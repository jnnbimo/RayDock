package system

import (
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetExtendedTcpTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

const (
	tcpTableBasicConnections = 1 // TCP_TABLE_BASIC_CONNECTIONS: no listeners
	tcpStateEstablished      = 5 // MIB_TCP_STATE_ESTAB
)

// Connections counts the established IPv4 TCP connections accepted on the local ports given, such as the
// relay port of a core finishing its connections before it stops. It returns -1 when the table cannot
// be read.
func Connections(ports ...int) int {
	var size uint32
	var buf []byte
	for range 3 { // the table may grow between the size query and the read
		var p unsafe.Pointer
		if len(buf) > 0 {
			p = unsafe.Pointer(&buf[0])
		}
		r, _, _ := procGetExtendedTcpTable.Call(uintptr(p), uintptr(unsafe.Pointer(&size)), 0, windows.AF_INET, tcpTableBasicConnections, 0)
		if r == 0 && len(buf) > 0 {
			return countRows(buf, ports)
		}
		if r != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) && r != 0 {
			return -1
		}
		buf = make([]byte, size+1024)
		size = uint32(len(buf))
	}
	return -1
}

// countRows reads a MIB_TCPTABLE: the number of rows, then rows of state, local address, local port,
// remote address and remote port, each a DWORD; ports are in network order.
func countRows(table []byte, ports []int) int {
	if len(table) < 4 {
		return -1
	}
	n := int(binary.LittleEndian.Uint32(table))
	count := 0
	for i := range n {
		row := table[4+i*20:]
		if len(row) < 20 {
			return -1
		}
		state := binary.LittleEndian.Uint32(row)
		local := int(binary.BigEndian.Uint16(row[8:10]))
		if state != tcpStateEstablished {
			continue
		}
		for _, p := range ports {
			if local == p {
				count++
			}
		}
	}
	return count
}
