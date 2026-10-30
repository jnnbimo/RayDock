package system

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetExtendedTcpTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

// ListenerOwned verifies an IPv4 listening socket against its actual process ID, not a past reservation.
func ListenerOwned(pid, port int) (bool, error) {
	var size uint32
	var buf []byte
	const ownerPIDListeners = 3 // TCP_TABLE_OWNER_PID_LISTENER; MIB_TCPROW_OWNER_PID
	for range 4 {
		var p unsafe.Pointer
		if len(buf) > 0 {
			p = unsafe.Pointer(&buf[0])
		}
		r, _, _ := procGetExtendedTcpTable.Call(uintptr(p), uintptr(unsafe.Pointer(&size)), 0, windows.AF_INET, ownerPIDListeners, 0)
		if r == 0 && len(buf) > 0 {
			if len(buf) < 4 {
				return false, fmt.Errorf("TCP 监听表格式错误")
			}
			n := int(binary.LittleEndian.Uint32(buf))
			if n > (len(buf)-4)/24 {
				return false, fmt.Errorf("TCP 监听表不完整")
			}
			for i := 0; i < n; i++ {
				row := buf[4+i*24:]
				if int(binary.BigEndian.Uint16(row[8:10])) == port && int(binary.LittleEndian.Uint32(row[20:24])) == pid {
					return true, nil
				}
			}
			return false, nil
		}
		if r != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) && r != 0 {
			return false, windows.Errno(r)
		}
		buf = make([]byte, size+1024)
		size = uint32(len(buf))
	}
	return false, fmt.Errorf("TCP 监听表持续变化，无法确认端口所有者")
}
