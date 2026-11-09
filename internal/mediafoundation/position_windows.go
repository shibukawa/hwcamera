//go:build windows && (amd64 || arm64)

package mediafoundation

import (
	"github.com/shibukawa/hwcamera/internal/camera"
	"github.com/shibukawa/hwcamera/internal/mediafoundation/sys"
)

// positionFor looks up the device node behind a camera's symbolic link and
// derives its position from the ACPI _PLD panel and the device container.
// Every lookup is optional; failures fall back to the symbolic link.
func positionFor(symlink string) camera.Position {
	panel := -1
	var inLocal *bool
	typ, data, ok := sys.DeviceInterfaceProperty(symlink, &sys.DEVPKEY_Device_InstanceId)
	if ok && typ == sys.DEVPROP_TYPE_STRING {
		if inst, ok := sys.LocateDevNode(sys.UTF16BytesToString(data)); ok {
			if typ, d, ok := sys.DevNodeProperty(inst, &sys.DEVPKEY_Device_InLocalMachineContainer); ok && typ == sys.DEVPROP_TYPE_BOOLEAN && len(d) > 0 {
				b := d[0] != 0
				inLocal = &b
			}
			// The camera's own _PLD; for a built-in camera also the one of
			// the port it hangs off (internal USB webcams describe the lid
			// there). An external camera's port says nothing about the
			// camera, so its parent is not consulted.
			node := inst
			for i := 0; i < 2; i++ {
				if typ, d, ok := sys.DevNodeProperty(node, &sys.DEVPKEY_Device_PhysicalDeviceLocation); ok && typ == sys.DEVPROP_TYPE_BINARY {
					if p := pldPanel(d); p >= 0 && p != panelUnknown {
						panel = p
						break
					}
				}
				if inLocal == nil || !*inLocal {
					break
				}
				parent, ok := sys.DevNodeParent(node)
				if !ok {
					break
				}
				node = parent
			}
		}
	}
	return positionOf(symlink, panel, inLocal)
}
