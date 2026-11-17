package mapper

import "math"

type province struct {
	code     string // kode wilayah
	name     string
	lat, lon float64
}

// Province centre points from BNPB's reference table (data.bnpb.go.id, dataset
// "Data Bencana Indonesia", resource faeb2406-b800-4b8e-9e09-37d4b2aa5879),
// copied as published. Names are written in title case, as in the Audit API example.
var provinces = []province{
	{"11", "Aceh", 4.6951, 96.9103},
	{"12", "Sumatera Utara", 2.1154, 99.8213},
	{"13", "Sumatera Barat", -0.7399, 100.441},
	{"14", "Riau", 0.5333, 101.4478},
	{"15", "Jambi", -1.6101, 103.6131},
	{"16", "Sumatera Selatan", -3.3194, 104.0957},
	{"17", "Bengkulu", -3.7928, 102.2655},
	{"18", "Lampung", -4.5586, 105.4068},
	{"19", "Kepulauan Bangka Belitung", -2.7411, 106.4406},
	{"21", "Kepulauan Riau", 3.9457, 108.1429},
	{"31", "DKI Jakarta", -6.2088, 106.8456},
	{"32", "Jawa Barat", -6.8897, 107.6405},
	{"33", "Jawa Tengah", -7.1509, 110.1403},
	{"34", "Daerah Istimewa Yogyakarta", -7.7956, 110.3695},
	{"35", "Jawa Timur", -7.2504, 112.7508},
	{"36", "Banten", -6.4058, 106.064},
	{"51", "Bali", -8.3405, 115.092},
	{"52", "Nusa Tenggara Barat", -8.6529, 116.3249},
	{"53", "Nusa Tenggara Timur", -10.1772, 123.6069},
	{"61", "Kalimantan Barat", 0.1218, 111.473},
	{"62", "Kalimantan Tengah", -1.6815, 113.3824},
	{"63", "Kalimantan Selatan", -3.0926, 115.2838},
	{"64", "Kalimantan Timur", 0.5247, 117.123},
	{"65", "Kalimantan Utara", 3.0731, 116.0414},
	{"71", "Sulawesi Utara", 1.4307, 124.8471},
	{"72", "Sulawesi Tengah", -1.43, 121.4456},
	{"73", "Sulawesi Selatan", -3.6688, 119.9741},
	{"74", "Sulawesi Tenggara", -4.1436, 122.1746},
	{"75", "Gorontalo", 0.5435, 123.0706},
	{"76", "Sulawesi Barat", -2.8445, 119.2321},
	{"81", "Maluku", -3.2385, 129.4922},
	{"82", "Maluku Utara", 1.5709, 127.8082},
	{"91", "Papua", -2.5337, 138.083},
	{"92", "Papua Barat", -1.3361, 133.1747},
	{"93", "Papua Selatan", -7.6711, 131.2757},
	{"94", "Papua Tengah", -3.8549, 136.0142},
	{"95", "Papua Pegunungan", -4.3924, 138.4683},
	{"96", "Papua Barat Daya", -1.0156, 132.268},
}

func NearestProvince(lat, lon float64) string {
	best, bestDistance := "", math.Inf(1)
	for _, p := range provinces {
		if d := DistanceKm(lat, lon, p.lat, p.lon); d < bestDistance {
			best, bestDistance = p.name, d
		}
	}
	return best
}
