//go:build darwin || windows

package main

type menuRenderCache[T comparable] struct {
	last T
	set  bool
}

func (c *menuRenderCache[T]) Apply(next T, render func(T)) bool {
	if c.set && c.last == next {
		return false
	}
	render(next)
	c.last = next
	c.set = true
	return true
}
