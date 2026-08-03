package service

import (
	"testing"
	"vpnbot/database"
)

// Второй инстанс живёт на той же машине, что и боевой, и обязан с ним разойтись:
// иначе он либо не стартует (занятый 9091), либо торчит наружу мимо туннеля.
func TestAltVariantНеКонфликтуетСБоевым(t *testing.T) {
	cfg := database.TelemetConfig{
		Port: 9443, TLSDomain: "lk.rt.ru",
		ClassicEnabled: true, SecureEnabled: true,
		AltTLSDomain: "cdn.moskva.live",
	}

	main, alt := mainVariant(cfg), altVariant(cfg)

	if main.Port == alt.Port {
		t.Errorf("инстансы делят порт %d — второй не стартует", main.Port)
	}
	if alt.Port != 19443 {
		t.Errorf("порт второго инстанса по умолчанию = %d, ожидался 19443", alt.Port)
	}
	if alt.APIEnabled {
		t.Error("API второго инстанса включён — займёт 9091 боевого")
	}
	if alt.ListenIP != "127.0.0.1" {
		t.Errorf("второй инстанс слушает %s, а он должен быть доступен только через туннель", alt.ListenIP)
	}
	if alt.ClassicOK || alt.SecureOK {
		t.Error("classic/secure включены на втором инстансе, хотя живут только на боевом входе")
	}
	if alt.TLSDomain != "cdn.moskva.live" || main.TLSDomain != "lk.rt.ru" {
		t.Errorf("имена разъехались: боевой=%q второй=%q", main.TLSDomain, alt.TLSDomain)
	}
}

func TestAltInstancePortПереопределяется(t *testing.T) {
	alt := altVariant(database.TelemetConfig{AltInstancePort: 19555})
	if alt.Port != 19555 {
		t.Errorf("порт = %d, ожидался 19555", alt.Port)
	}
}

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
