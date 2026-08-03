package service

import "testing"

// Три режима MTProto различаются только префиксом секрета, и перепутать их легко:
// клиент молча не подключится, а в логах это будет неотличимо от блокировки.
func TestTelemetLinkFormats(t *testing.T) {
	const (
		addr   = "cdn.moskva.live"
		secret = "39aa15242dd555a1b08c5715a71f1261"
	)

	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			// ee + секрет + домен в hex: "lk.rt.ru" → 6c6b2e72742e7275
			name: "fake tls",
			got:  GenerateTelemetProxyLink(addr, 443, secret, "lk.rt.ru"),
			want: "tg://proxy?server=cdn.moskva.live&port=443&secret=ee" + secret + "6c6b2e72742e7275",
		},
		{
			name: "secure",
			got:  GenerateTelemetSecureLink(addr, 9443, secret),
			want: "tg://proxy?server=cdn.moskva.live&port=9443&secret=dd" + secret,
		},
		{
			name: "classic",
			got:  GenerateTelemetClassicLink(addr, 9443, secret),
			want: "tg://proxy?server=cdn.moskva.live&port=9443&secret=" + secret,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("ссылка не совпала:\n got: %s\nwant: %s", tc.got, tc.want)
			}
		})
	}
}
