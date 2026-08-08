package main

import "testing"

// TestPartDeviceName štiti destruktivni put: krivo ime particije na NVMe/eMMC
// disku značilo bi da se nakon zapisa u tablicu radi s pogrešnim uređajem.
func TestPartDeviceName(t *testing.T) {
	cases := map[string]struct {
		disk string
		num  int
		want string
	}{
		"sata":  {"sda", 3, "sda3"},
		"sata2": {"sdb", 1, "sdb1"},
		"nvme":  {"nvme0n1", 3, "nvme0n1p3"},
		"emmc":  {"mmcblk0", 2, "mmcblk0p2"},
		"vd":    {"vda", 4, "vda4"},
	}
	for name, c := range cases {
		if got := partDeviceName(c.disk, c.num); got != c.want {
			t.Errorf("%s: partDeviceName(%q,%d)=%q, očekivano %q",
				name, c.disk, c.num, got, c.want)
		}
	}
}
