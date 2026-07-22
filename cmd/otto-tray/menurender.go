//go:build darwin || windows

package main

type menuItemRenderOps struct {
	setTitle   func(string)
	setEnabled func(bool)
	setVisible func(bool)
}

type gatewayMenuRenderOps struct {
	setIcon    func(State)
	setTooltip func(string)
	header     menuItemRenderOps
	subheader  menuItemRenderOps
	start      menuItemRenderOps
	stop       menuItemRenderOps
	restart    menuItemRenderOps
}

type desktopMenuRenderOps struct {
	header     menuItemRenderOps
	appFolder  menuItemRenderOps
	dataFolder menuItemRenderOps
	install    menuItemRenderOps
	start      menuItemRenderOps
	stop       menuItemRenderOps
}

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
