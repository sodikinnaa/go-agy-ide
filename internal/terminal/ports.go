package terminal

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ActivePort struct {
	Port    int    `json:"port"`
	IP      string `json:"ip"`
	PID     int    `json:"pid,omitempty"`
	Process string `json:"process,omitempty"`
}

type rawPortEntry struct {
	IP    string
	Port  int
	Inode string
}

// GetActivePorts lists all listening TCP ports on the system
func GetActivePorts() ([]ActivePort, error) {
	ports, err := parseProcNetTCP("/proc/net/tcp")
	if err != nil {
		ports = []rawPortEntry{}
	}

	ports6, err := parseProcNetTCP("/proc/net/tcp6")
	if err == nil {
		ports = append(ports, ports6...)
	}

	return resolveProcesses(ports)
}

func parseProcNetTCP(path string) ([]rawPortEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []rawPortEntry
	scanner := bufio.NewScanner(file)
	if scanner.Scan() {
		_ = scanner.Text() // Skip header
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		state := fields[3]
		if state != "0A" { // TCP_LISTEN
			continue
		}

		localAddr := fields[1]
		parts := strings.Split(localAddr, ":")
		if len(parts) != 2 {
			continue
		}

		ipHex := parts[0]
		portHex := parts[1]

		port, err := strconv.ParseInt(portHex, 16, 32)
		if err != nil {
			continue
		}

		ip := parseHexIP(ipHex)
		inode := fields[9]

		entries = append(entries, rawPortEntry{
			IP:    ip,
			Port:  int(port),
			Inode: inode,
		})
	}

	return entries, nil
}

func parseHexIP(hexStr string) string {
	if len(hexStr) == 8 {
		b1, _ := strconv.ParseInt(hexStr[6:8], 16, 0)
		b2, _ := strconv.ParseInt(hexStr[4:6], 16, 0)
		b3, _ := strconv.ParseInt(hexStr[2:4], 16, 0)
		b4, _ := strconv.ParseInt(hexStr[0:2], 16, 0)
		return fmt.Sprintf("%d.%d.%d.%d", b1, b2, b3, b4)
	} else if len(hexStr) == 32 {
		var ip net.IP = make([]byte, 16)
		for i := 0; i < 16; i++ {
			b, _ := strconv.ParseInt(hexStr[i*2:(i+1)*2], 16, 0)
			ip[i] = byte(b)
		}
		var parts []string
		for i := 0; i < 4; i++ {
			sub := hexStr[i*8 : (i+1)*8]
			b1 := sub[6:8]
			b2 := sub[4:6]
			b3 := sub[2:4]
			b4 := sub[0:2]
			parts = append(parts, b1+b2, b3+b4)
		}
		parsedIP := net.ParseIP(strings.Join(parts, ":"))
		if parsedIP != nil {
			return parsedIP.String()
		}
	}
	return hexStr
}

func resolveProcesses(entries []rawPortEntry) ([]ActivePort, error) {
	inodeToEntry := make(map[string]*ActivePort)
	var result []ActivePort

	for _, entry := range entries {
		ap := ActivePort{
			Port: entry.Port,
			IP:   entry.IP,
		}
		result = append(result, ap)
		if entry.Inode != "0" {
			inodeToEntry[entry.Inode] = &result[len(result)-1]
		}
	}

	files, err := ioutil.ReadDir("/proc")
	if err != nil {
		return result, nil // Fallback to ports only
	}

	for _, file := range files {
		if !file.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(file.Name())
		if err != nil {
			continue
		}

		fdDir := filepath.Join("/proc", file.Name(), "fd")
		fds, err := ioutil.ReadDir(fdDir)
		if err != nil {
			continue
		}

		for _, fd := range fds {
			fdPath := filepath.Join(fdDir, fd.Name())
			link, err := os.Readlink(fdPath)
			if err != nil {
				continue
			}

			if strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") {
				inode := link[8 : len(link)-1]
				if ap, ok := inodeToEntry[inode]; ok {
					ap.PID = pid
					ap.Process = getProcessName(pid)
				}
			}
		}
	}

	return result, nil
}

func getProcessName(pid int) string {
	commPath := fmt.Sprintf("/proc/%d/comm", pid)
	bytes, err := ioutil.ReadFile(commPath)
	if err == nil {
		return strings.TrimSpace(string(bytes))
	}

	cmdlinePath := fmt.Sprintf("/proc/%d/cmdline", pid)
	bytes, err = ioutil.ReadFile(cmdlinePath)
	if err == nil && len(bytes) > 0 {
		parts := strings.Split(string(bytes), "\x00")
		if len(parts) > 0 && parts[0] != "" {
			return filepath.Base(parts[0])
		}
	}

	return fmt.Sprintf("PID %d", pid)
}
