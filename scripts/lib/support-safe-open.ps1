# scripts/lib/support-safe-open.ps1 — native no-follow source boundary for
# PowerShell 5.1+ support collection. Dot-sourced by scripts/gw.ps1 and tests.

function Initialize-SupportSafeOpen {
    if ('GatewaySupport.SafeFile' -as [type]) { return }

    Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;

namespace GatewaySupport {
    public sealed class SourceMetadata {
        public string Identity { get; private set; }
        public long Length { get; private set; }
        public DateTime LastWriteTimeUtc { get; private set; }

        public SourceMetadata(string identity, long length, DateTime lastWriteTimeUtc) {
            Identity = identity;
            Length = length;
            LastWriteTimeUtc = lastWriteTimeUtc;
        }
    }

    public sealed class OpenedSource : IDisposable {
        public FileStream Stream { get; private set; }
        public SourceMetadata Metadata { get; private set; }

        public OpenedSource(FileStream stream, SourceMetadata metadata) {
            Stream = stream;
            Metadata = metadata;
        }

        public void Dispose() {
            if (Stream != null) {
                Stream.Dispose();
                Stream = null;
            }
        }
    }

    public static class SafeFile {
        const uint FILE_READ_DATA = 0x00000001;
        const uint FILE_READ_ATTRIBUTES = 0x00000080;
        const uint SYNCHRONIZE = 0x00100000;
        const uint FILE_SHARE_READ = 0x00000001;
        const uint FILE_SHARE_WRITE = 0x00000002;
        const uint FILE_SHARE_DELETE = 0x00000004;
        const uint FILE_OPEN = 1;
        const uint FILE_NON_DIRECTORY_FILE = 0x00000040;
        const uint FILE_SYNCHRONOUS_IO_NONALERT = 0x00000020;
        const uint FILE_OPEN_REPARSE_POINT = 0x00200000;
        const uint OBJ_CASE_INSENSITIVE = 0x00000040;
        const uint OBJ_DONT_REPARSE = 0x00001000;
        const uint FILE_ATTRIBUTE_REPARSE_POINT = 0x00000400;
        const uint FILE_TYPE_DISK = 0x0001;
        const uint S_IFMT = 0xF000;
        const uint S_IFREG = 0x8000;

        [StructLayout(LayoutKind.Sequential)]
        struct UnicodeString {
            public ushort Length;
            public ushort MaximumLength;
            public IntPtr Buffer;
        }

        [StructLayout(LayoutKind.Sequential)]
        struct ObjectAttributes {
            public int Length;
            public IntPtr RootDirectory;
            public IntPtr ObjectName;
            public uint Attributes;
            public IntPtr SecurityDescriptor;
            public IntPtr SecurityQualityOfService;
        }

        [StructLayout(LayoutKind.Sequential)]
        struct IoStatusBlock {
            public IntPtr Status;
            public IntPtr Information;
        }

        [StructLayout(LayoutKind.Sequential)]
        struct ByHandleFileInformation {
            public uint FileAttributes;
            public System.Runtime.InteropServices.ComTypes.FILETIME CreationTime;
            public System.Runtime.InteropServices.ComTypes.FILETIME LastAccessTime;
            public System.Runtime.InteropServices.ComTypes.FILETIME LastWriteTime;
            public uint VolumeSerialNumber;
            public uint FileSizeHigh;
            public uint FileSizeLow;
            public uint NumberOfLinks;
            public uint FileIndexHigh;
            public uint FileIndexLow;
        }

        [StructLayout(LayoutKind.Explicit, Size = 144)]
        struct DarwinStat {
            [FieldOffset(0)] public int Device;
            [FieldOffset(4)] public ushort Mode;
            [FieldOffset(8)] public ulong Inode;
            [FieldOffset(48)] public long ModifiedSeconds;
            [FieldOffset(56)] public long ModifiedNanoseconds;
            [FieldOffset(96)] public long Size;
        }

        [StructLayout(LayoutKind.Explicit, Size = 144)]
        struct LinuxX64Stat {
            [FieldOffset(0)] public ulong Device;
            [FieldOffset(8)] public ulong Inode;
            [FieldOffset(24)] public uint Mode;
            [FieldOffset(48)] public long Size;
            [FieldOffset(88)] public long ModifiedSeconds;
            [FieldOffset(96)] public long ModifiedNanoseconds;
        }

        [StructLayout(LayoutKind.Explicit, Size = 128)]
        struct LinuxArm64Stat {
            [FieldOffset(0)] public ulong Device;
            [FieldOffset(8)] public ulong Inode;
            [FieldOffset(16)] public uint Mode;
            [FieldOffset(48)] public long Size;
            [FieldOffset(88)] public long ModifiedSeconds;
            [FieldOffset(96)] public ulong ModifiedNanoseconds;
        }

        [DllImport("ntdll.dll")]
        static extern int NtCreateFile(out IntPtr fileHandle, uint desiredAccess,
            ref ObjectAttributes objectAttributes, out IoStatusBlock ioStatusBlock,
            IntPtr allocationSize, uint fileAttributes, uint shareAccess,
            uint createDisposition, uint createOptions, IntPtr eaBuffer,
            uint eaLength);

        [DllImport("ntdll.dll")]
        static extern uint RtlNtStatusToDosError(int status);

        [DllImport("kernel32.dll", SetLastError = true)]
        static extern bool GetFileInformationByHandle(SafeFileHandle handle,
            out ByHandleFileInformation information);

        [DllImport("kernel32.dll", SetLastError = true)]
        static extern uint GetFileType(SafeFileHandle handle);

        [DllImport("libc", SetLastError = true)]
        static extern int open(string path, int flags);

        [DllImport("libc", SetLastError = true)]
        static extern int openat(int directory, string path, int flags);

        [DllImport("libc", SetLastError = true)]
        static extern int close(int descriptor);

        [DllImport("libc", EntryPoint = "fstat", SetLastError = true)]
        static extern int FstatDarwin(int descriptor, out DarwinStat stat);

        [DllImport("libc", EntryPoint = "fstat", SetLastError = true)]
        static extern int FstatLinuxX64(int descriptor, out LinuxX64Stat stat);

        [DllImport("libc", EntryPoint = "fstat", SetLastError = true)]
        static extern int FstatLinuxArm64(int descriptor, out LinuxArm64Stat stat);

        static readonly DateTime UnixEpoch =
            new DateTime(1970, 1, 1, 0, 0, 0, DateTimeKind.Utc);

        public static string DescribeUnixAbi(string operatingSystem, string architecture) {
            string os = (operatingSystem ?? "").Trim().ToLowerInvariant();
            string arch = (architecture ?? "").Trim().ToLowerInvariant();
            if (os == "darwin" && arch == "x64") return "darwin-x64";
            if (os == "darwin" && arch == "arm64") return "darwin-arm64";
            if (os == "linux" && arch == "x64") return "linux-x64";
            if (os == "linux" && arch == "arm64") return "linux-arm64";
            throw new PlatformNotSupportedException(
                "safe-open has no verified Unix ABI for " + operatingSystem + "/" + architecture);
        }

        static string RuntimeUnixAbi() {
            string os;
            if (RuntimeInformation.IsOSPlatform(OSPlatform.OSX)) os = "Darwin";
            else if (RuntimeInformation.IsOSPlatform(OSPlatform.Linux)) os = "Linux";
            else throw new PlatformNotSupportedException("safe-open Unix platform is unsupported");
            return DescribeUnixAbi(os, RuntimeInformation.ProcessArchitecture.ToString());
        }

        static DateTime UnixTime(long seconds, long nanoseconds) {
            return UnixEpoch.AddSeconds(seconds).AddTicks(nanoseconds / 100);
        }

        static string NtPath(string path) {
            string full = Path.GetFullPath(path);
            if (full.StartsWith(@"\\?\UNC\", StringComparison.OrdinalIgnoreCase))
                return @"\??\UNC\" + full.Substring(8);
            if (full.StartsWith(@"\\?\", StringComparison.OrdinalIgnoreCase))
                return @"\??\" + full.Substring(4);
            if (full.StartsWith(@"\\", StringComparison.Ordinal))
                return @"\??\UNC\" + full.Substring(2);
            return @"\??\" + full;
        }

        static SafeFileHandle OpenWindowsNoFollow(string path) {
            string nativePath = NtPath(path);
            IntPtr stringBuffer = Marshal.StringToHGlobalUni(nativePath);
            IntPtr unicodePointer = IntPtr.Zero;
            try {
                UnicodeString unicode = new UnicodeString();
                unicode.Length = checked((ushort)(nativePath.Length * 2));
                unicode.MaximumLength = checked((ushort)(unicode.Length + 2));
                unicode.Buffer = stringBuffer;
                unicodePointer = Marshal.AllocHGlobal(Marshal.SizeOf(typeof(UnicodeString)));
                Marshal.StructureToPtr(unicode, unicodePointer, false);

                ObjectAttributes attributes = new ObjectAttributes();
                attributes.Length = Marshal.SizeOf(typeof(ObjectAttributes));
                attributes.ObjectName = unicodePointer;
                attributes.Attributes = OBJ_CASE_INSENSITIVE | OBJ_DONT_REPARSE;
                IoStatusBlock statusBlock;
                IntPtr rawHandle;
                int status = NtCreateFile(out rawHandle,
                    FILE_READ_DATA | FILE_READ_ATTRIBUTES | SYNCHRONIZE,
                    ref attributes, out statusBlock, IntPtr.Zero, 0,
                    FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                    FILE_OPEN,
                    FILE_NON_DIRECTORY_FILE | FILE_SYNCHRONOUS_IO_NONALERT |
                        FILE_OPEN_REPARSE_POINT,
                    IntPtr.Zero, 0);
                if (status < 0) ThrowNtOpen(status, path);
                return new SafeFileHandle(rawHandle, true);
            } finally {
                if (unicodePointer != IntPtr.Zero) Marshal.FreeHGlobal(unicodePointer);
                Marshal.FreeHGlobal(stringBuffer);
            }
        }

        static void ThrowNtOpen(int status, string path) {
            const int STATUS_REPARSE_POINT_ENCOUNTERED = unchecked((int)0xC000050B);
            if (status == STATUS_REPARSE_POINT_ENCOUNTERED)
                throw new InvalidDataException("reparse point");
            uint error = RtlNtStatusToDosError(status);
            if (error == 5) throw new UnauthorizedAccessException("safe-open access denied");
            if (error == 2 || error == 3)
                throw new FileNotFoundException("source replaced before safe-open", path);
            if (error == 4390 || error == 4393 || error == 4394 || error == 1920)
                throw new InvalidDataException("reparse point");
            throw new IOException("safe-open failed (NTSTATUS 0x" + status.ToString("X8") + ")");
        }

        static SafeFileHandle OpenUnixNoFollow(string path, string abi) {
            string full = Path.GetFullPath(path);
            string[] components = full.Split(new char[] { '/' }, StringSplitOptions.RemoveEmptyEntries);
            bool darwin = abi.StartsWith("darwin-", StringComparison.Ordinal);
            int noFollow = darwin ? 0x00000100 : 0x00020000;
            int nonBlock = darwin ? 0x00000004 : 0x00000800;
            int directory = darwin ? 0x00100000 : 0x00010000;
            int closeOnExec = darwin ? 0x01000000 : 0x00080000;
            int current = open("/", directory | noFollow | closeOnExec);
            if (current < 0) ThrowUnixOpen(Marshal.GetLastWin32Error(), path);
            try {
                for (int index = 0; index < components.Length; index++) {
                    bool final = index == components.Length - 1;
                    int flags = noFollow | closeOnExec |
                        (final ? nonBlock : directory);
                    int next = openat(current, components[index], flags);
                    if (next < 0) ThrowUnixOpen(Marshal.GetLastWin32Error(), path);
                    close(current);
                    current = next;
                }
                SafeFileHandle result = new SafeFileHandle(new IntPtr(current), true);
                current = -1;
                return result;
            } finally {
                if (current >= 0) close(current);
            }
        }

        static void ThrowUnixOpen(int error, string path) {
            if (error == 13) throw new UnauthorizedAccessException("safe-open access denied");
            if (error == 2) throw new FileNotFoundException("source replaced before safe-open", path);
            if (error == 20 || error == 40 || error == 62)
                throw new InvalidDataException("reparse point");
            throw new IOException("safe-open failed (errno " + error + ")");
        }

        static SourceMetadata WindowsMetadata(SafeFileHandle handle) {
            ByHandleFileInformation information;
            if (!GetFileInformationByHandle(handle, out information))
                throw new IOException("safe-open inspection failed (Win32 " + Marshal.GetLastWin32Error() + ")");
            if ((information.FileAttributes & FILE_ATTRIBUTE_REPARSE_POINT) != 0)
                throw new InvalidDataException("reparse point");
            if (GetFileType(handle) != FILE_TYPE_DISK)
                throw new InvalidDataException("not a regular file");
            string identity = information.VolumeSerialNumber.ToString("X8") + ":" +
                information.FileIndexHigh.ToString("X8") + information.FileIndexLow.ToString("X8");
            long length = ((long)information.FileSizeHigh << 32) | information.FileSizeLow;
            long fileTime = ((long)information.LastWriteTime.dwHighDateTime << 32) |
                (uint)information.LastWriteTime.dwLowDateTime;
            return new SourceMetadata(identity, length, DateTime.FromFileTimeUtc(fileTime));
        }

        static SourceMetadata UnixMetadata(SafeFileHandle handle, string abi) {
            int descriptor = handle.DangerousGetHandle().ToInt32();
            uint mode;
            ulong device;
            ulong inode;
            long length;
            long modifiedSeconds;
            long modifiedNanoseconds;
            int result;
            if (abi.StartsWith("darwin-", StringComparison.Ordinal)) {
                DarwinStat stat;
                result = FstatDarwin(descriptor, out stat);
                mode = stat.Mode;
                device = unchecked((uint)stat.Device);
                inode = stat.Inode;
                length = stat.Size;
                modifiedSeconds = stat.ModifiedSeconds;
                modifiedNanoseconds = stat.ModifiedNanoseconds;
            } else if (abi == "linux-x64") {
                LinuxX64Stat stat;
                result = FstatLinuxX64(descriptor, out stat);
                mode = stat.Mode;
                device = stat.Device;
                inode = stat.Inode;
                length = stat.Size;
                modifiedSeconds = stat.ModifiedSeconds;
                modifiedNanoseconds = stat.ModifiedNanoseconds;
            } else {
                LinuxArm64Stat stat;
                result = FstatLinuxArm64(descriptor, out stat);
                mode = stat.Mode;
                device = stat.Device;
                inode = stat.Inode;
                length = stat.Size;
                modifiedSeconds = stat.ModifiedSeconds;
                modifiedNanoseconds = checked((long)stat.ModifiedNanoseconds);
            }
            if (result != 0)
                throw new IOException("safe-open inspection failed (errno " + Marshal.GetLastWin32Error() + ")");
            if ((mode & S_IFMT) != S_IFREG)
                throw new InvalidDataException("not a regular file");
            return new SourceMetadata(device.ToString("X") + ":" + inode.ToString("X"),
                length, UnixTime(modifiedSeconds, modifiedNanoseconds));
        }

        static SafeFileHandle OpenHandle(string path, out SourceMetadata metadata) {
            if (Environment.OSVersion.Platform == PlatformID.Win32NT) {
                SafeFileHandle handle = OpenWindowsNoFollow(path);
                try {
                    metadata = WindowsMetadata(handle);
                    return handle;
                } catch {
                    handle.Dispose();
                    throw;
                }
            }
            string abi = RuntimeUnixAbi();
            SafeFileHandle unixHandle = OpenUnixNoFollow(path, abi);
            try {
                metadata = UnixMetadata(unixHandle, abi);
                return unixHandle;
            } catch {
                unixHandle.Dispose();
                throw;
            }
        }

        public static SourceMetadata InspectRegularNoFollow(string path) {
            SourceMetadata metadata;
            using (SafeFileHandle handle = OpenHandle(path, out metadata)) { }
            return metadata;
        }

        public static OpenedSource OpenRegularNoFollow(string path, string expectedIdentity) {
            SourceMetadata metadata;
            SafeFileHandle handle = OpenHandle(path, out metadata);
            if (!String.Equals(metadata.Identity, expectedIdentity, StringComparison.Ordinal)) {
                handle.Dispose();
                throw new IOException("source replaced before safe-open");
            }
            try {
                FileStream stream = new FileStream(handle, FileAccess.Read, 65536, false);
                return new OpenedSource(stream, metadata);
            } catch {
                handle.Dispose();
                throw;
            }
        }
    }
}
'@
}

function Get-SupportUnixAbiLayout {
    param([string]$OperatingSystem, [string]$Architecture)
    return [GatewaySupport.SafeFile]::DescribeUnixAbi($OperatingSystem, $Architecture)
}
