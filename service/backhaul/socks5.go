package backhaul

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// socks5Dialer — минимальный SOCKS5-клиент без аутентификации.
// Пишем свой, а не берём готовый, ровно по одной причине: нужен явный контроль
// над тем, как передаётся адрес — ATYP=DOMAINNAME (имя резолвит удалённая
// сторона, remote DNS) или ATYP=IPv4 (резолв локальный). Проверка remote DNS
// входит в требования, и подменять её «как получится» нельзя.
type socks5Dialer struct {
	proxyAddr string
	timeout   time.Duration
}

const (
	socks5Version   = 0x05
	socks5NoAuth    = 0x00
	socks5CmdConn   = 0x01
	socks5ATYPv4    = 0x01
	socks5ATYPName  = 0x03
	socks5ATYPv6    = 0x04
	socks5Succeeded = 0x00
)

var socks5Errors = map[byte]string{
	0x01: "general SOCKS server failure",
	0x02: "connection not allowed by ruleset",
	0x03: "network unreachable",
	0x04: "host unreachable",
	0x05: "connection refused",
	0x06: "TTL expired",
	0x07: "command not supported",
	0x08: "address type not supported",
}

// DialContext устанавливает CONNECT через SOCKS5.
//
// Если host — доменное имя, оно уходит на прокси как есть (remote DNS).
// Если это литеральный IP — уходит как ATYP=IPv4/IPv6.
func (d *socks5Dialer) DialContext(ctx context.Context, _network, addr string) (net.Conn, error) {
	host, portS, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("socks5: разбор адреса %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portS)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("socks5: некорректный порт в %q", addr)
	}

	dialer := net.Dialer{Timeout: d.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", d.proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("socks5: подключение к адаптеру %s: %w", d.proxyAddr, err)
	}

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(d.timeout))
	}

	if err := d.handshake(conn, host, port); err != nil {
		conn.Close()
		return nil, err
	}
	// Дедлайн рукопожатия снимаем: дальше временем управляет stallConn.
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func (d *socks5Dialer) handshake(conn net.Conn, host string, port int) error {
	if _, err := conn.Write([]byte{socks5Version, 1, socks5NoAuth}); err != nil {
		return fmt.Errorf("socks5: greeting: %w", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("socks5: ответ на greeting: %w", err)
	}
	if reply[0] != socks5Version {
		return fmt.Errorf("socks5: неожиданная версия %d", reply[0])
	}
	if reply[1] != socks5NoAuth {
		return fmt.Errorf("socks5: адаптер требует аутентификацию (method=%d)", reply[1])
	}

	req := []byte{socks5Version, socks5CmdConn, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			req = append(req, socks5ATYPv4)
			req = append(req, v4...)
		} else {
			req = append(req, socks5ATYPv6)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return fmt.Errorf("socks5: слишком длинное имя хоста")
		}
		req = append(req, socks5ATYPName, byte(len(host)))
		req = append(req, host...)
	}
	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], uint16(port))
	req = append(req, portBuf[:]...)

	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("socks5: CONNECT: %w", err)
	}

	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return fmt.Errorf("socks5: ответ на CONNECT: %w", err)
	}
	if head[1] != socks5Succeeded {
		if msg, ok := socks5Errors[head[1]]; ok {
			return fmt.Errorf("socks5: CONNECT отклонён: %s", msg)
		}
		return fmt.Errorf("socks5: CONNECT отклонён, код %d", head[1])
	}
	// Дочитываем BND.ADDR/BND.PORT, иначе они останутся в потоке.
	switch head[3] {
	case socks5ATYPv4:
		_, err := io.ReadFull(conn, make([]byte, 4+2))
		return err
	case socks5ATYPv6:
		_, err := io.ReadFull(conn, make([]byte, 16+2))
		return err
	case socks5ATYPName:
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return err
		}
		_, err := io.ReadFull(conn, make([]byte, int(l[0])+2))
		return err
	default:
		return fmt.Errorf("socks5: неизвестный ATYP %d в ответе", head[3])
	}
}

// stallConn — соединение, которое падает, если между двумя успешными
// операциями чтения/записи прошло больше stall. Именно это отличает
// «канал медленный» от «канал повис»: TCP-соединение может оставаться
// установленным сколь угодно долго, не передав ни байта.
type stallConn struct {
	net.Conn
	stall time.Duration
}

func (c *stallConn) Read(b []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.stall)); err != nil {
		return 0, err
	}
	return c.Conn.Read(b)
}

func (c *stallConn) Write(b []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(c.stall)); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}
