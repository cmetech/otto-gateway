package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/admin"
)

type fakeCaptureSource struct {
	frames    []admin.CaptureFrame
	enabled   bool
	allow     bool
	size      int
	enableN   int
	disableN  int
	clearN    int
	snapshotN int
}

func (f *fakeCaptureSource) Snapshot() []admin.CaptureFrame { f.snapshotN++; return f.frames }
func (f *fakeCaptureSource) Enabled() bool                  { return f.enabled }
func (f *fakeCaptureSource) AllowRuntimeToggle() bool       { return f.allow }
func (f *fakeCaptureSource) Count() int                     { return len(f.frames) }
func (f *fakeCaptureSource) Size() int                      { return f.size }
func (f *fakeCaptureSource) Enable()                        { f.enableN++; f.enabled = true }
func (f *fakeCaptureSource) Disable()                       { f.disableN++; f.enabled = false }
func (f *fakeCaptureSource) Clear()                         { f.clearN++; f.frames = nil }

func doCapturePost(t *testing.T, src admin.AcpCaptureSource, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := admin.Handler(admin.Deps{AcpCapture: src})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/acp-capture", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func doGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
	return rec
}

// TestAcpCapture_Enabled: with a source wired, the endpoint returns enabled:true
// and the frames as JSON.
func TestAcpCapture_Enabled(t *testing.T) {
	src := &fakeCaptureSource{enabled: true, frames: []admin.CaptureFrame{
		{Seq: 1, Ts: time.Unix(1700000000, 0).UTC(), Method: "session/update", Params: `{"x":1}`, Bytes: 7},
	}}
	h := admin.Handler(admin.Deps{AcpCapture: src})

	rec := doGet(t, h, "/api/acp-capture")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Enabled bool                 `json:"enabled"`
		Frames  []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !body.Enabled {
		t.Error("enabled = false, want true")
	}
	if len(body.Frames) != 1 || body.Frames[0].Method != "session/update" {
		t.Errorf("frames = %+v", body.Frames)
	}
}

// TestAcpCapture_Disabled: with no source, the endpoint reports enabled:false and
// an empty (non-nil) frames array.
func TestAcpCapture_Disabled(t *testing.T) {
	h := admin.Handler(admin.Deps{})
	rec := doGet(t, h, "/api/acp-capture")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Enabled bool                 `json:"enabled"`
		Frames  []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Enabled {
		t.Error("enabled = true, want false when no source wired")
	}
	if body.Frames == nil {
		t.Error("frames must be a non-nil (empty) array")
	}
}

// TestAcpCaptureSupport_RedactsDecodedJSONWithoutMutatingCapture catches a
// support export that either leaks a nested credential, destroys safe
// diagnostic context, produces invalid outer JSON, or edits the source ring.
func TestAcpCaptureSupport_RedactsDecodedJSONWithoutMutatingCapture(t *testing.T) {
	const params = `{
		"safe":"safe-value",
		"keyboardLayout":"qwerty-safe",
		"nested":{
			"Authorization":"Bearer named_auth_secret",
			"api_key":"glc_secret",
			"token":"named_token_secret",
			"Key":"named_key_secret",
			"secret":"named_secret_value",
			"password":"named_password_secret",
			"passphrase":"named_passphrase_secret",
			"headers":{"X-Trace":"safe-header","X-API-Key":"named_header_secret"}
		},
		"notes":[
			"safe-prefix Authorization: Bearer abc.def safe-suffix",
			"safe-prefix Basic dXNlcjp0b2tlbg== safe-suffix",
			"safe-prefix X-API-Key: inline_api_secret safe-suffix",
			"safe sentence"
		],
		"encoded":"{\"apiKey\":\"encoded_api_secret\",\"safe\":\"encoded-safe\"}"
	}`
	src := &fakeCaptureSource{enabled: true, allow: true, size: 32, frames: []admin.CaptureFrame{
		{Seq: 7, Ts: time.Unix(1700000000, 0).UTC(), Method: "session/update", Params: params, Bytes: len(params)},
	}}
	wantSource := append([]admin.CaptureFrame(nil), src.frames...)
	h := admin.Handler(admin.Deps{AcpCapture: src})

	rec := doGet(t, h, "/api/acp-capture?support=redacted")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Enabled bool                 `json:"enabled"`
		Frames  []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("support export is invalid JSON: %v; body=%s", err, rec.Body.String())
	}
	if !body.Enabled || len(body.Frames) != 1 {
		t.Fatalf("support export shape = %+v, want one enabled frame", body)
	}
	if body.Frames[0].Seq != 7 || body.Frames[0].Method != "session/update" {
		t.Fatalf("non-sensitive frame metadata changed: %+v", body.Frames[0])
	}

	for _, secret := range []string{
		"named_auth_secret", "glc_secret", "named_token_secret",
		"named_key_secret", "named_secret_value", "named_password_secret",
		"named_passphrase_secret", "named_header_secret", "abc.def",
		"dXNlcjp0b2tlbg==", "inline_api_secret", "encoded_api_secret",
	} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("support export leaked secret %q", secret)
		}
	}
	for _, safe := range []string{"safe-value", "qwerty-safe", "safe-header", "safe-prefix", "safe-suffix", "safe sentence", "encoded-safe"} {
		if !strings.Contains(rec.Body.String(), safe) {
			t.Errorf("support export removed safe diagnostic value %q", safe)
		}
	}

	var redactedParams map[string]any
	if err := json.Unmarshal([]byte(body.Frames[0].Params), &redactedParams); err != nil {
		t.Fatalf("redacted frame params are not valid JSON: %v; params=%s", err, body.Frames[0].Params)
	}
	if got := redactedParams["safe"]; got != "safe-value" {
		t.Errorf("safe param = %v, want safe-value", got)
	}
	if !strings.Contains(body.Frames[0].Params, "[REDACTED]") {
		t.Errorf("redacted params contain no redaction marker: %s", body.Frames[0].Params)
	}
	if src.snapshotN != 1 {
		t.Errorf("Snapshot calls = %d, want one ordinary snapshot", src.snapshotN)
	}
	if src.enableN != 0 || src.disableN != 0 || src.clearN != 0 {
		t.Errorf("support GET called mutator: enable=%d disable=%d clear=%d", src.enableN, src.disableN, src.clearN)
	}
	if !reflect.DeepEqual(src.frames, wantSource) {
		t.Errorf("support export mutated source frames:\n got: %+v\nwant: %+v", src.frames, wantSource)
	}
}

// TestAcpCaptureSupport_MalformedParamsBecomeScrubbedString catches a fallback
// that returns malformed captured params verbatim or corrupts the response JSON.
func TestAcpCaptureSupport_MalformedParamsBecomeScrubbedString(t *testing.T) {
	const params = `malformed safe-value Authorization: Bearer malformed_bearer_secret X-API-Key=malformed_api_secret`
	src := &fakeCaptureSource{enabled: true, frames: []admin.CaptureFrame{{Seq: 1, Params: params}}}
	h := admin.Handler(admin.Deps{AcpCapture: src})

	rec := doGet(t, h, "/api/acp-capture?support=redacted")
	var body struct {
		Frames []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("support export is invalid JSON: %v; body=%s", err, rec.Body.String())
	}
	if len(body.Frames) != 1 {
		t.Fatalf("frames = %+v, want one", body.Frames)
	}
	if strings.Contains(body.Frames[0].Params, "malformed_bearer_secret") || strings.Contains(body.Frames[0].Params, "malformed_api_secret") {
		t.Errorf("malformed params leaked a credential: %q", body.Frames[0].Params)
	}
	for _, safe := range []string{"malformed", "safe-value"} {
		if !strings.Contains(body.Frames[0].Params, safe) {
			t.Errorf("malformed fallback removed safe text %q: %q", safe, body.Frames[0].Params)
		}
	}
	if src.frames[0].Params != params {
		t.Errorf("malformed fallback mutated source params: got %q, want %q", src.frames[0].Params, params)
	}
}

// TestAcpCaptureSupport_ScrubsNamingConventionsInsideStrings catches
// separator- and camel-case credential labels leaking from malformed params or
// JSON encoded inside a decoded string leaf.
func TestAcpCaptureSupport_ScrubsNamingConventionsInsideStrings(t *testing.T) {
	const malformed = `malformed-safe access_token=malformed_access_shh refreshToken=malformed_refresh_shh client_secret=malformed_client_shh dbPassword=malformed_password_shh`
	const encoded = `{"payload":"{\"access_token\":\"encoded_access_shh\",\"refreshToken\":\"encoded_refresh_shh\",\"client_secret\":\"encoded_client_shh\",\"dbPassword\":\"encoded_password_shh\",\"safe\":\"encoded-safe\"}"}`
	src := &fakeCaptureSource{enabled: true, frames: []admin.CaptureFrame{
		{Seq: 1, Params: malformed},
		{Seq: 2, Params: encoded},
	}}
	h := admin.Handler(admin.Deps{AcpCapture: src})

	rec := doGet(t, h, "/api/acp-capture?support=redacted")
	var body struct {
		Frames []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("support export is invalid JSON: %v; body=%s", err, rec.Body.String())
	}
	if len(body.Frames) != 2 {
		t.Fatalf("frames = %+v, want two", body.Frames)
	}
	for _, secret := range []string{
		"malformed_access_shh", "malformed_refresh_shh", "malformed_client_shh", "malformed_password_shh",
		"encoded_access_shh", "encoded_refresh_shh", "encoded_client_shh", "encoded_password_shh",
	} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("support export leaked naming-convention credential %q", secret)
		}
	}
	for _, safe := range []string{"malformed-safe", "encoded-safe"} {
		if !strings.Contains(rec.Body.String(), safe) {
			t.Errorf("support export removed safe string %q", safe)
		}
	}
}

// TestAcpCaptureSupport_ScrubsEveryQueryAssignment catches a safe leading
// form/query parameter consuming later credential assignments as part of its
// value and preventing those later names from being examined.
func TestAcpCaptureSupport_ScrubsEveryQueryAssignment(t *testing.T) {
	const params = `{
		"query":"safe=ok&access_token=query_access_shh&after=kept&token=query_token_shh&last=visible",
		"form":"first=visible&password=form_password_shh&final=kept"
	}`
	src := &fakeCaptureSource{enabled: true, frames: []admin.CaptureFrame{{Seq: 1, Params: params}}}
	h := admin.Handler(admin.Deps{AcpCapture: src})

	rec := doGet(t, h, "/api/acp-capture?support=redacted")
	var body struct {
		Frames []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("support export is invalid JSON: %v; body=%s", err, rec.Body.String())
	}
	if len(body.Frames) != 1 {
		t.Fatalf("frames = %+v, want one", body.Frames)
	}
	for _, secret := range []string{"query_access_shh", "query_token_shh", "form_password_shh"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("support export leaked chained query credential %q", secret)
		}
	}
	var redactedParams map[string]string
	if err := json.Unmarshal([]byte(body.Frames[0].Params), &redactedParams); err != nil {
		t.Fatalf("redacted params are invalid JSON: %v; params=%s", err, body.Frames[0].Params)
	}
	for _, safe := range []string{"safe=ok", "after=kept", "last=visible"} {
		if !strings.Contains(redactedParams["query"], safe) {
			t.Errorf("query lost safe parameter %q: %s", safe, redactedParams["query"])
		}
	}
	for _, safe := range []string{"first=visible", "final=kept"} {
		if !strings.Contains(redactedParams["form"], safe) {
			t.Errorf("form lost safe parameter %q: %s", safe, redactedParams["form"])
		}
	}
}

// TestAcpCaptureSupport_SemanticCredentialNameSegments catches credential
// terms followed by value-bearing suffixes while protecting similarly named
// metadata and ordinary words in both parsed keys and string assignments.
func TestAcpCaptureSupport_SemanticCredentialNameSegments(t *testing.T) {
	const params = `{
		"accessTokenValue":"parsed_access_token_value_shh",
		"passwordHash":"parsed_password_hash_shh",
		"clientSecretValue":"parsed_client_secret_value_shh",
		"apiKeyHash":"parsed_api_key_hash_shh",
		"authorizationHeader":"parsed_authorization_header_shh",
		"safe":{"tokenCount":7,"secretary":"safe-secretary","keyboardLayout":"safe-keyboard"},
		"assignmentText":"tokenCount=9 secretary=office-safe keyboardLayout=qwerty-safe accessTokenValue=string_access_token_value_shh passwordHash=string_password_hash_shh clientSecretValue=string_client_secret_value_shh"
	}`
	src := &fakeCaptureSource{enabled: true, frames: []admin.CaptureFrame{{Seq: 1, Params: params}}}
	h := admin.Handler(admin.Deps{AcpCapture: src})

	rec := doGet(t, h, "/api/acp-capture?support=redacted")
	var body struct {
		Frames []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("support export is invalid JSON: %v; body=%s", err, rec.Body.String())
	}
	if len(body.Frames) != 1 {
		t.Fatalf("frames = %+v, want one", body.Frames)
	}
	for _, secret := range []string{
		"parsed_access_token_value_shh", "parsed_password_hash_shh", "parsed_client_secret_value_shh",
		"parsed_api_key_hash_shh", "parsed_authorization_header_shh", "string_access_token_value_shh",
		"string_password_hash_shh", "string_client_secret_value_shh",
	} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("support export leaked semantic-name credential %q", secret)
		}
	}
	for _, safe := range []string{
		`"tokenCount":7`, "safe-secretary", "safe-keyboard",
		"tokenCount=9", "secretary=office-safe", "keyboardLayout=qwerty-safe",
	} {
		if !strings.Contains(body.Frames[0].Params, safe) {
			t.Errorf("support export removed safe semantic-name value %q; params=%s", safe, body.Frames[0].Params)
		}
	}
}

// TestAcpCaptureSupport_ScrubsEntireGenericAuthorizationValue catches schemes
// other than Bearer/Basic leaving credential material after the scheme token.
func TestAcpCaptureSupport_ScrubsEntireGenericAuthorizationValue(t *testing.T) {
	const params = `{"notes":[
		"apikey-safe-prefix\nAuthorization: ApiKey generic_apikey_shh\napikey-safe-suffix",
		"digest-safe-prefix\nAuthorization: Digest username=alice, realm=example, nonce=digest_nonce_shh, response=digest_response_shh\ndigest-safe-suffix",
		"aws-safe-prefix\nAuthorization: AWS4-HMAC-SHA256 Credential=aws_credential_shh/20260727/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=aws_signature_shh\naws-safe-suffix"
	],"encoded":"{\"Authorization\":\"Digest username=alice, nonce=encoded_digest_shh\",\"safe\":\"encoded-auth-safe\"}"}`
	src := &fakeCaptureSource{enabled: true, frames: []admin.CaptureFrame{{Seq: 1, Params: params}}}
	h := admin.Handler(admin.Deps{AcpCapture: src})

	rec := doGet(t, h, "/api/acp-capture?support=redacted")
	var body struct {
		Frames []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("support export is invalid JSON: %v; body=%s", err, rec.Body.String())
	}
	for _, secret := range []string{
		"generic_apikey_shh", "digest_nonce_shh", "digest_response_shh",
		"aws_credential_shh", "aws_signature_shh", "encoded_digest_shh",
	} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("support export leaked Authorization credential %q", secret)
		}
	}
	for _, safe := range []string{
		"apikey-safe-prefix", "apikey-safe-suffix", "digest-safe-prefix",
		"digest-safe-suffix", "aws-safe-prefix", "aws-safe-suffix", "encoded-auth-safe",
	} {
		if !strings.Contains(rec.Body.String(), safe) {
			t.Errorf("support export removed surrounding safe text %q", safe)
		}
	}
	var redactedParams struct {
		Encoded string `json:"encoded"`
	}
	if err := json.Unmarshal([]byte(body.Frames[0].Params), &redactedParams); err != nil {
		t.Fatalf("redacted params are invalid JSON: %v; params=%s", err, body.Frames[0].Params)
	}
	var encoded map[string]any
	if err := json.Unmarshal([]byte(redactedParams.Encoded), &encoded); err != nil {
		t.Fatalf("redaction damaged encoded Authorization JSON: %v; encoded=%s", err, redactedParams.Encoded)
	}
	if got := encoded["safe"]; got != "encoded-auth-safe" {
		t.Errorf("encoded safe value = %v, want encoded-auth-safe", got)
	}
}

// TestAcpCaptureSupport_ParsedKeyNamingConventions catches acronym credential
// suffixes leaking and ordinary words or metadata prefixes being over-redacted.
func TestAcpCaptureSupport_ParsedKeyNamingConventions(t *testing.T) {
	const params = `{
		"signingKEY":"signing_key_shh",
		"access_token":"access_token_shh",
		"refreshToken":"refresh_token_shh",
		"client_secret":"client_secret_shh",
		"dbPassword":"db_password_shh",
		"Authorization":"authorization_shh",
		"bearer":"bearer_shh",
		"apiKey":"api_key_shh",
		"secretary":"safe-secretary",
		"tokenCount":42,
		"keyboardLayout":"safe-keyboard"
	}`
	src := &fakeCaptureSource{enabled: true, frames: []admin.CaptureFrame{{Seq: 1, Params: params}}}
	h := admin.Handler(admin.Deps{AcpCapture: src})

	rec := doGet(t, h, "/api/acp-capture?support=redacted")
	var body struct {
		Frames []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("support export is invalid JSON: %v; body=%s", err, rec.Body.String())
	}
	for _, secret := range []string{
		"signing_key_shh", "access_token_shh", "refresh_token_shh", "client_secret_shh",
		"db_password_shh", "authorization_shh", "bearer_shh", "api_key_shh",
	} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("support export leaked parsed credential %q", secret)
		}
	}
	for _, safe := range []string{"safe-secretary", `"tokenCount":42`, "safe-keyboard"} {
		if !strings.Contains(body.Frames[0].Params, safe) {
			t.Errorf("support export removed safe parsed value %q; params=%s", safe, body.Frames[0].Params)
		}
	}
}

// TestAcpCaptureSupport_PreservesLargeJSONInteger catches JSON decoding through
// float64 changing an otherwise safe integer during support export.
func TestAcpCaptureSupport_PreservesLargeJSONInteger(t *testing.T) {
	const params = `{"requestId":9007199254740993,"safe":"numeric-safe"}`
	src := &fakeCaptureSource{enabled: true, frames: []admin.CaptureFrame{{Seq: 1, Params: params}}}
	h := admin.Handler(admin.Deps{AcpCapture: src})

	rec := doGet(t, h, "/api/acp-capture?support=redacted")
	var body struct {
		Frames []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("support export is invalid JSON: %v; body=%s", err, rec.Body.String())
	}
	if len(body.Frames) != 1 {
		t.Fatalf("frames = %+v, want one", body.Frames)
	}
	if !strings.Contains(body.Frames[0].Params, `"requestId":9007199254740993`) {
		t.Errorf("large JSON integer changed during support export: %s", body.Frames[0].Params)
	}
	if !strings.Contains(body.Frames[0].Params, "numeric-safe") {
		t.Errorf("safe numeric context removed: %s", body.Frames[0].Params)
	}
}

// TestAcpCaptureSupport_RecursivelyRedactsJSONStringLeaves catches a redactor
// that treats decoded string leaves as flat text. Flat assignment matching can
// redact only the first word of a quoted value and leaks object/array values;
// walking each bounded JSON document keeps the whole secret value opaque.
func TestAcpCaptureSupport_RecursivelyRedactsJSONStringLeaves(t *testing.T) {
	deepest := `{"password":"deep secret with \"quoted\" text","safe":"deep-safe"}`
	levelTwoBody, err := json.Marshal(map[string]string{"payload": deepest, "safe": "level-two-safe"})
	if err != nil {
		t.Fatal(err)
	}
	levelOneBody, err := json.Marshal(map[string]string{"payload": string(levelTwoBody), "safe": "level-one-safe"})
	if err != nil {
		t.Fatal(err)
	}
	paramsBody, err := json.Marshal(map[string]string{
		"multiword": `{"password":"multi word secret","safe":"multi-safe"}`,
		"object":    `{"password":{"nested":"object-value-secret"},"safe":"object-safe"}`,
		"array":     `{"api_key":["array-value-secret",{"nested":"array-object-secret"}],"safe":"array-safe"}`,
		"recursive": string(levelOneBody),
	})
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeCaptureSource{enabled: true, frames: []admin.CaptureFrame{{Seq: 1, Params: string(paramsBody)}}}
	h := admin.Handler(admin.Deps{AcpCapture: src})

	rec := doGet(t, h, "/api/acp-capture?support=redacted")
	var body struct {
		Frames []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("support export is invalid JSON: %v; body=%s", err, rec.Body.String())
	}
	if len(body.Frames) != 1 {
		t.Fatalf("frames = %+v, want one", body.Frames)
	}
	for _, secret := range []string{
		"multi word secret", "object-value-secret", "array-value-secret",
		"array-object-secret", "deep secret", "quoted",
	} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("support export leaked recursively encoded secret %q", secret)
		}
	}
	for _, safe := range []string{
		"multi-safe", "object-safe", "array-safe", "level-one-safe",
		"level-two-safe", "deep-safe",
	} {
		if !strings.Contains(rec.Body.String(), safe) {
			t.Errorf("support export removed recursively encoded safe value %q", safe)
		}
	}

	var params map[string]string
	if err := json.Unmarshal([]byte(body.Frames[0].Params), &params); err != nil {
		t.Fatalf("redacted params are invalid JSON: %v; params=%s", err, body.Frames[0].Params)
	}
	for _, name := range []string{"multiword", "object", "array", "recursive"} {
		var nested any
		if err := json.Unmarshal([]byte(params[name]), &nested); err != nil {
			t.Errorf("redaction damaged nested JSON leaf %q: %v; leaf=%s", name, err, params[name])
		}
	}
}

// TestAcpCaptureSupport_MalformedAssignmentsRedactWholeQuotedAndStructuredValues
// catches fallback parsing that stops at whitespace or the first structural
// delimiter, exposing the remainder of a secret value. The fixture is
// intentionally not JSON so it exercises the quote-aware fail-safe parser.
func TestAcpCaptureSupport_MalformedAssignmentsRedactWholeQuotedAndStructuredValues(t *testing.T) {
	const params = `malformed-safe password="multi word malformed secret" after=visible ` +
		`api_key={"nested":"object malformed secret"} ` +
		`refreshToken=["array malformed secret",{"more":"array object malformed secret"}] ` +
		`dbPassword="escaped malformed secret with \"quoted secret\" tail" ` +
		`password_confirmation='confirmation malformed secret' final=kept`
	src := &fakeCaptureSource{enabled: true, frames: []admin.CaptureFrame{{Seq: 1, Params: params}}}
	h := admin.Handler(admin.Deps{AcpCapture: src})

	rec := doGet(t, h, "/api/acp-capture?support=redacted")
	var body struct {
		Frames []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("support export is invalid JSON: %v; body=%s", err, rec.Body.String())
	}
	if len(body.Frames) != 1 {
		t.Fatalf("frames = %+v, want one", body.Frames)
	}
	for _, secret := range []string{
		"multi word malformed secret", "object malformed secret",
		"array malformed secret", "array object malformed secret",
		"escaped malformed secret", "quoted secret", "confirmation malformed secret",
	} {
		if strings.Contains(body.Frames[0].Params, secret) {
			t.Errorf("malformed fallback partially exposed secret %q: %s", secret, body.Frames[0].Params)
		}
	}
	for _, safe := range []string{"malformed-safe", "after=visible", "final=kept"} {
		if !strings.Contains(body.Frames[0].Params, safe) {
			t.Errorf("malformed fallback removed safe text %q: %s", safe, body.Frames[0].Params)
		}
	}
}

// TestAcpCaptureSupport_NestedJSONLimitsFailClosed catches unbounded recursive
// decoding as well as a limit path that returns an uninspected encoded secret.
// The response must stay valid JSON and replace the bounded-away subtree as a
// whole, never expose part of the credential.
func TestAcpCaptureSupport_NestedJSONLimitsFailClosed(t *testing.T) {
	deep := `{"password":"depth-limit-secret"}`
	// Re-encoding a JSON document as a JSON string necessarily doubles escape
	// runs, so twelve layers are ample to cross the production recursion bound
	// without turning the fixture itself into an exponential memory test.
	for range 12 {
		body, err := json.Marshal(map[string]string{"payload": deep})
		if err != nil {
			t.Fatal(err)
		}
		deep = string(body)
	}
	oversizedSecret := "oversized-limit-secret-" + strings.Repeat("x", 300*1024)
	oversizedBody, err := json.Marshal(map[string]string{"password": oversizedSecret})
	if err != nil {
		t.Fatal(err)
	}
	paramsBody, err := json.Marshal(map[string]string{"deep": deep, "oversized": string(oversizedBody), "safe": "limit-safe"})
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeCaptureSource{enabled: true, frames: []admin.CaptureFrame{{Seq: 1, Params: string(paramsBody)}}}
	h := admin.Handler(admin.Deps{AcpCapture: src})

	rec := doGet(t, h, "/api/acp-capture?support=redacted")
	var body struct {
		Frames []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bounded support export is invalid JSON: %v; body prefix=%q", err, rec.Body.String()[:min(256, rec.Body.Len())])
	}
	if len(body.Frames) != 1 {
		t.Fatalf("frames = %+v, want one", body.Frames)
	}
	for _, secret := range []string{"depth-limit-secret", "oversized-limit-secret-"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("bounded nested JSON path leaked secret prefix %q", secret)
		}
	}
	if !strings.Contains(body.Frames[0].Params, "limit-safe") {
		t.Errorf("bounded redaction removed outer safe context: %s", body.Frames[0].Params)
	}
}

// TestAcpCaptureSupport_OrdinaryGetRemainsRaw catches accidental redaction of
// the existing operator-only capture endpoint when support mode is not asked for.
func TestAcpCaptureSupport_OrdinaryGetRemainsRaw(t *testing.T) {
	const params = `{"token":"ordinary_get_secret","safe":"safe-value"}`
	src := &fakeCaptureSource{enabled: true, frames: []admin.CaptureFrame{{Seq: 1, Params: params}}}
	h := admin.Handler(admin.Deps{AcpCapture: src})

	rec := doGet(t, h, "/api/acp-capture")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ordinary_get_secret") {
		t.Errorf("ordinary GET was unexpectedly redacted: %s", rec.Body.String())
	}
	if src.frames[0].Params != params {
		t.Errorf("ordinary GET mutated source params: got %q, want %q", src.frames[0].Params, params)
	}
}

// TestAcpCaptureSupport_DisabledAndEmptyRemainEmpty catches support mode
// changing the endpoint's stable non-nil empty-array contract.
func TestAcpCaptureSupport_DisabledAndEmptyRemainEmpty(t *testing.T) {
	tests := []struct {
		name string
		src  admin.AcpCaptureSource
	}{
		{name: "disabled", src: nil},
		{name: "enabled empty", src: &fakeCaptureSource{enabled: true, frames: nil}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := admin.Handler(admin.Deps{AcpCapture: tc.src})
			rec := doGet(t, h, "/api/acp-capture?support=redacted")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			var body struct {
				Frames []admin.CaptureFrame `json:"frames"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("support export is invalid JSON: %v", err)
			}
			if body.Frames == nil || len(body.Frames) != 0 {
				t.Errorf("frames = %#v, want non-nil empty array", body.Frames)
			}
		})
	}
}

// TestAbout_AcpCaptureRow: the About page's Feature Flags card shows an
// "ACP capture" row reflecting whether ACP_CAPTURE is on — "on" (with the
// SENSITIVE badge) when a capture source is wired, "off" when it is nil. The
// wired-vs-nil AcpCaptureSource is the canonical enabled signal.
func TestAbout_AcpCaptureRow(t *testing.T) {
	const onRow = `<dt>ACP capture</dt><dd>on <span class="gw-badge is-warning">SENSITIVE</span></dd>`
	const offRow = `<dt>ACP capture</dt><dd>off</dd>`

	// Wired source → on.
	hOn := admin.Handler(admin.Deps{AcpCapture: &fakeCaptureSource{enabled: true}})
	recOn := doGet(t, hOn, "/about")
	if recOn.Code != http.StatusOK {
		t.Fatalf("GET /about (wired): status = %d, want 200", recOn.Code)
	}
	if body := recOn.Body.String(); !strings.Contains(body, onRow) {
		t.Errorf("About page missing ACP-capture ON row %q", onRow)
	}

	// No source → off.
	hOff := admin.Handler(admin.Deps{})
	recOff := doGet(t, hOff, "/about")
	if recOff.Code != http.StatusOK {
		t.Fatalf("GET /about (nil): status = %d, want 200", recOff.Code)
	}
	if body := recOff.Body.String(); !strings.Contains(body, offRow) {
		t.Errorf("About page missing ACP-capture OFF row %q", offRow)
	}
}

// TestDocs_AcpCaptureRows: the operator Docs page env-var table documents
// ACP_CAPTURE and ACP_CAPTURE_SIZE (added so the diagnostics flag is
// discoverable alongside the other env vars).
func TestDocs_AcpCaptureRows(t *testing.T) {
	h := admin.Handler(admin.Deps{})
	rec := doGet(t, h, "/docs")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /docs: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"ACP_CAPTURE", "ACP_CAPTURE_SIZE"} {
		if !strings.Contains(body, want) {
			t.Errorf("Docs page env table missing %q row", want)
		}
	}
}

func TestAcpCapturePost_EnableWhenAllowed(t *testing.T) {
	src := &fakeCaptureSource{allow: true, size: 512}
	rec := doCapturePost(t, src, `{"action":"enable"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if src.enableN != 1 || !src.enabled {
		t.Fatalf("enable not applied: enableN=%d enabled=%v", src.enableN, src.enabled)
	}
}

func TestAcpCapturePost_ForbiddenWhenToggleDisallowed(t *testing.T) {
	src := &fakeCaptureSource{allow: false}
	rec := doCapturePost(t, src, `{"action":"enable"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when allow=false, got %d", rec.Code)
	}
	if src.enableN != 0 {
		t.Fatalf("enable applied despite 403: enableN=%d", src.enableN)
	}
}

func TestAcpCapturePost_UnknownAction400(t *testing.T) {
	src := &fakeCaptureSource{allow: true}
	rec := doCapturePost(t, src, `{"action":"frobnicate"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown action, got %d", rec.Code)
	}
}

func TestAcpCaptureGet_ExtendedShape(t *testing.T) {
	src := &fakeCaptureSource{allow: true, enabled: true, size: 512, frames: []admin.CaptureFrame{{Seq: 1, Method: "session/update"}}}
	h := admin.Handler(admin.Deps{AcpCapture: src})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/acp-capture", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got struct {
		Enabled            bool                 `json:"enabled"`
		AllowRuntimeToggle bool                 `json:"allowRuntimeToggle"`
		Count              int                  `json:"count"`
		Size               int                  `json:"size"`
		Frames             []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Enabled || !got.AllowRuntimeToggle || got.Count != 1 || got.Size != 512 || len(got.Frames) != 1 {
		t.Fatalf("extended GET shape wrong: %+v", got)
	}
}

// TestAcpCapturePost_RejectsNonJSONContentType: a POST without an
// application/json Content-Type must be rejected with 415 before the body is
// decoded or any mutator runs — this closes off a cross-origin "simple
// request" (text/plain) form-POST that would otherwise bypass CORS
// preflight and blind-toggle capture.
func TestAcpCapturePost_RejectsNonJSONContentType(t *testing.T) {
	src := &fakeCaptureSource{allow: true}
	h := admin.Handler(admin.Deps{AcpCapture: src})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/acp-capture", strings.NewReader(`{"action":"enable"}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415 for non-JSON Content-Type, got %d; body=%s", rec.Code, rec.Body.String())
	}
	if src.enableN != 0 {
		t.Fatalf("enable applied despite non-JSON Content-Type: enableN=%d", src.enableN)
	}
}
