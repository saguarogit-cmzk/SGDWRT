package main

import (
	"encoding/binary"
	"net"
)

// Dodjela adresa u VPN tunelu.
//
// Adrese se dodjeljuju redom, od prve slobodne prema kraju mreže, da se ne mora
// ručno pamtiti tko je što dobio. Rezervirane su: mrežna adresa (prva),
// adresa samog uređaja u tunelu (druga) i broadcast (zadnja).

func ipv4ToUint(ip net.IP) (uint32, bool) {
	v4 := ip.To4()
	if v4 == nil {
		return 0, false
	}
	return binary.BigEndian.Uint32(v4), true
}

func uintToIPv4(v uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return net.IP(b[:]).String()
}

// tunnelHostRange vraća prvu i zadnju adresu koja se smije dodijeliti klijentu.
func tunnelHostRange(n *net.IPNet) (first, last uint32, ok bool) {
	if n == nil {
		return 0, 0, false
	}
	base, okB := ipv4ToUint(n.IP)
	ones, bits := n.Mask.Size()
	// /30 i uže nemaju mjesta ni za jednog klijenta uz rezervirane adrese
	if !okB || bits != 32 || ones > 29 || ones < 8 {
		return 0, 0, false
	}
	size := uint32(1) << uint(32-ones)
	return base + 2, base + size - 2, true
}

// tunnelIPReserved javlja je li adresa u mreži, ali se ne smije dodijeliti
// klijentu (mrežna, adresa uređaja u tunelu ili broadcast).
func tunnelIPReserved(n *net.IPNet, ip net.IP) bool {
	first, last, ok := tunnelHostRange(n)
	if !ok {
		return false
	}
	v, okIP := ipv4ToUint(ip)
	if !okIP {
		return false
	}
	return v < first || v > last
}

// nextFreeTunnelIP vraća prvu slobodnu adresu u mreži tunela; prazno ako je
// mreža nepoznata ili popunjena.
func nextFreeTunnelIP(n *net.IPNet, used map[string]bool) string {
	first, last, ok := tunnelHostRange(n)
	if !ok {
		return ""
	}
	for v := first; v <= last; v++ {
		ip := uintToIPv4(v)
		if !used[ip] {
			return ip
		}
	}
	return ""
}

// usedTunnelIPs čita zauzete adrese iz zadane tablice.
func (s *server) usedTunnelIPs(table string) map[string]bool {
	used := map[string]bool{}
	rows, err := s.db.Query(`SELECT tunnel_ip FROM ` + table)
	if err != nil {
		return used
	}
	defer rows.Close()
	for rows.Next() {
		var ip string
		if rows.Scan(&ip) == nil {
			used[ip] = true
		}
	}
	return used
}
