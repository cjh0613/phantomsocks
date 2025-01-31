package phantomtcp

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

func ReadAtLeast() {

}

func SocksProxy(client net.Conn) {
	defer client.Close()

	var conn net.Conn
	{
		var b [1500]byte
		n, err := client.Read(b[:])
		if err != nil || n < 3 {
			logPrintln(1, client.RemoteAddr(), err)
			return
		}

		host := ""
		var ip net.IP
		port := 0
		var reply []byte
		if b[0] == 0x05 {
			client.Write([]byte{0x05, 0x00})
			n, err = client.Read(b[:4])
			if err != nil || n != 4 {
				return
			}
			switch b[3] {
			case 0x01: //IPv4
				n, err = client.Read(b[:6])
				if n < 6 {
					return
				}
				ip = net.IP(b[:4])
				port = int(binary.BigEndian.Uint16(b[4:6]))
			case 0x03: //Domain
				n, err = client.Read(b[:])
				addrLen := b[0]
				if n < int(addrLen+3) {
					return
				}
				host = string(b[1 : addrLen+1])
				port = int(binary.BigEndian.Uint16(b[n-2:]))
			case 0x04: //IPv6
				n, err = client.Read(b[:])
				if n < 18 {
					return
				}
				ip = net.IP(b[:16])
				port = int(binary.BigEndian.Uint16(b[16:18]))
			default:
				logPrintln(1, "not supported")
				return
			}
			reply = []byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}
		} else if b[0] == 0x04 {
			if n > 8 && b[1] == 1 {
				userEnd := 8 + bytes.IndexByte(b[8:n], 0)
				port = int(binary.BigEndian.Uint16(b[2:4]))
				if b[4]|b[5]|b[6] == 0 {
					hostEnd := bytes.IndexByte(b[userEnd+1:n], 0)
					if hostEnd > 0 {
						host = string(b[userEnd+1 : userEnd+1+hostEnd])
					} else {
						client.Write([]byte{0, 91, 0, 0, 0, 0, 0, 0})
						return
					}
				} else {
					if b[4] == VirtualAddrPrefix {
						index := int(binary.BigEndian.Uint32(b[6:8]))
						if index >= len(Nose) {
							return
						}
						host = Nose[index]
					} else {
						ip = net.IP(b[4:8])
					}
				}

				reply = []byte{0, 90, b[2], b[3], b[4], b[5], b[6], b[7]}
			} else {
				client.Write([]byte{0, 91, 0, 0, 0, 0, 0, 0})
				return
			}
		} else {
			return
		}

		if err != nil {
			logPrintln(1, err)
			return
		}

		if host != "" {
			server := ConfigLookup(host)
			if server.Option == 0 {
				logPrintln(1, "Socks:", host, port, server)
				addr := net.JoinHostPort(host, strconv.Itoa(port))
				logPrintln(1, "Socks:", addr)
				conn, err = net.Dial("tcp", addr)
				if err != nil {
					logPrintln(1, err)
					return
				}
				_, err = client.Write(reply)
			} else if server.Proxy == "" {
				logPrintln(1, "Socks:", host, port, server)
				_, ips := NSLookup(host, server.Option, server.Server)
				if len(ips) == 0 {
					logPrintln(1, host, "no such host")
					return
				}
				_, err = client.Write(reply)
				if err != nil {
					logPrintln(1, err)
					return
				}

				n, err = client.Read(b[:])
				if err != nil {
					logPrintln(1, err)
					return
				}

				if b[0] != 0x16 {
					if server.Option&OPT_HTTP3 != 0 {
						HttpMove(client, "h3", b[:n])
						return
					} else if server.Option&OPT_HTTPS != 0 {
						HttpMove(client, "https", b[:n])
						return
					} else if server.Option&OPT_MOVE != 0 {
						HttpMove(client, server.Server, b[:n])
						return
					} else if server.Option&OPT_STRIP != 0 {
						rand.Seed(time.Now().UnixNano())
						ipaddr := ips[rand.Intn(len(ips))]
						if server.Option&OPT_FRONTING != 0 {
							host = ""
						}
						conn, err = DialStrip(ipaddr.String(), host)
						if err != nil {
							logPrintln(1, err)
							return
						}
						_, err = conn.Write(b[:n])
					} else {
						conn, err = server.HTTP(client, ips, port, b[:n])
						if err != nil {
							logPrintln(1, err)
							return
						}
						io.Copy(client, conn)
						return
					}
				} else {
					conn, err = server.Dial(ips, port, b[:n])
					if err != nil {
						logPrintln(1, host, err)
						return
					}
				}
			} else {
				logPrintln(1, "SocksoverProxy:", client.RemoteAddr(), "->", host, port, server)

				if server.Option&OPT_MODIFY != 0 {
					_, err = client.Write(reply)
					if err != nil {
						conn.Close()
						logPrintln(1, err)
						return
					}

					n, err = client.Read(b[:])
					if err != nil {
						logPrintln(1, err)
						return
					}

					conn, err = server.DialProxy(net.JoinHostPort(host, strconv.Itoa(port)), b[:n])
				} else {
					conn, err = server.DialProxy(net.JoinHostPort(host, strconv.Itoa(port)), nil)
					if err != nil {
						logPrintln(1, host, err)
						return
					}

					_, err = client.Write(reply)
				}
			}
		} else {
			if ip.To4() != nil {
				server := ConfigLookup(ip.String())
				addr := net.TCPAddr{IP: ip, Port: port, Zone: ""}
				if server.Option != 0 {
					logPrintln(1, "Socks:", addr.IP.String(), addr.Port, server)
					client.Write(reply)
					n, err = client.Read(b[:])
					if err != nil {
						logPrintln(1, err)
						return
					}

					ip := addr.IP
					result, ok := DNSCache.Load(ip.String())
					var addresses []net.IP
					if ok {
						records := result.(*DNSRecords)
						if records.AAAA != nil {
							addresses = make([]net.IP, len(records.AAAA.Addresses))
							copy(addresses, records.AAAA.Addresses)
						} else if records.A != nil {
							addresses = make([]net.IP, len(records.A.Addresses))
							copy(addresses, records.A.Addresses)
						}
					} else {
						addresses = []net.IP{ip}
					}
					conn, err = server.Dial(addresses, port, b[:n])
				} else {
					logPrintln(1, "Socks:", addr.IP.String(), addr.Port)

					conn, err = net.DialTCP("tcp", nil, &addr)
					client.Write(reply)
				}
			} else {
				addr := net.TCPAddr{IP: ip, Port: port, Zone: ""}
				logPrintln(1, "Socks:", addr.IP.String(), addr.Port)
				conn, err = net.DialTCP("tcp", nil, &addr)
				client.Write(reply)
			}
		}

		if err != nil {
			logPrintln(1, err)
			return
		}
	}

	defer conn.Close()

	_, _, err := relay(client, conn)
	if err != nil {
		if err, ok := err.(net.Error); ok && err.Timeout() {
			return
		}
		logPrintln(1, "relay error:", err)
	}
}

func validOptionalPort(port string) bool {
	if port == "" {
		return true
	}
	if port[0] != ':' {
		return false
	}
	for _, b := range port[1:] {
		if b < '0' || b > '9' {
			return false
		}
	}
	return true
}

func splitHostPort(hostport string) (host string, port int) {
	var err error
	host = hostport
	port = 0

	colon := strings.LastIndexByte(host, ':')
	if colon != -1 && validOptionalPort(host[colon:]) {
		port, err = strconv.Atoi(host[colon+1:])
		if err != nil {
			port = 0
		}
		host = host[:colon]
	}

	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}

	return
}

func SNIProxy(client net.Conn) {
	defer client.Close()

	var conn net.Conn
	{
		var b [1500]byte
		n, err := client.Read(b[:])
		if err != nil {
			log.Println(err)
			return
		}

		var host string
		var port int
		if b[0] == 0x16 {
			offset, length := GetSNI(b[:n])
			if length == 0 {
				return
			}
			host = string(b[offset : offset+length])
			port = 443
		} else {
			offset, length := GetHost(b[:n])
			if length == 0 {
				return
			}
			host = string(b[offset : offset+length])
			portstart := strings.Index(host, ":")
			if portstart == -1 {
				port = 80
			} else {
				port, err = strconv.Atoi(host[portstart+1:])
				if err != nil {
					return
				}
				host = host[:portstart]
			}
			if net.ParseIP(host) != nil {
				return
			}
		}

		server := ConfigLookup(host)
		if server.Option != 0 {
			logPrintln(1, "SNI:", host, port, server)

			_, ips := NSLookup(host, server.Option, server.Server)
			if len(ips) == 0 {
				logPrintln(1, host, "no such host")
				return
			}

			if b[0] == 0x16 {
				conn, err = server.Dial(ips, port, b[:n])
				if err != nil {
					logPrintln(1, host, err)
					return
				}
			} else {
				if server.Option&OPT_HTTP3 != 0 {
					HttpMove(client, "h3", b[:n])
					return
				} else if server.Option&OPT_HTTPS != 0 {
					HttpMove(client, "https", b[:n])
					return
				} else if server.Option&OPT_MOVE != 0 {
					HttpMove(client, server.Server, b[:n])
					return
				} else if server.Option&OPT_STRIP != 0 {
					ip := ips[rand.Intn(len(ips))]
					if server.Option&OPT_FRONTING != 0 {
						host = ""
					}
					conn, err = DialStrip(ip.String(), host)
					if err != nil {
						logPrintln(1, err)
						return
					}
					_, err = conn.Write(b[:n])
					if err != nil {
						logPrintln(1, err)
						return
					}
				} else {
					conn, err = server.HTTP(client, ips, port, b[:n])
					if err != nil {
						logPrintln(1, err)
						return
					}
					io.Copy(client, conn)
					return
				}
			}
		} else {
			host = net.JoinHostPort(host, strconv.Itoa(port))
			logPrintln(1, host)

			conn, err = net.Dial("tcp", host)
			if err != nil {
				logPrintln(1, err)
				return
			}
			_, err = conn.Write(b[:n])
			if err != nil {
				logPrintln(1, err)
				return
			}
		}
	}

	defer conn.Close()

	_, _, err := relay(client, conn)
	if err != nil {
		if err, ok := err.(net.Error); ok && err.Timeout() {
			return
		}
		logPrintln(1, "relay error:", err)
	}
}

func RedirectProxy(client net.Conn) {
	defer client.Close()

	var conn net.Conn
	{
		var host string
		var port int
		var ips []net.IP = nil
		addr, err := GetOriginalDST(client.(*net.TCPConn))
		if err != nil {
			logPrintln(1, err)
			return
		}

		switch addr.IP[0] {
		case 0x00:
			index := int(binary.BigEndian.Uint32(addr.IP[12:16]))
			if index >= len(Nose) {
				return
			}
			host = Nose[index]
		case VirtualAddrPrefix:
			index := int(binary.BigEndian.Uint16(addr.IP[2:4]))
			if index >= len(Nose) {
				return
			}
			host = Nose[index]
		default:
			if addr.String() == client.LocalAddr().String() {
				return
			}
			host = addr.IP.String()
			ips = []net.IP{addr.IP}
		}
		port = addr.Port

		server := ConfigLookup(host)
		if server.Option&OPT_NOTCP != 0 {
			return
		}

		if server.Proxy != "" {
			logPrintln(1, "RedirectProxy:", client.RemoteAddr(), "->", host, port, server)

			if server.Option == OPT_NONE {
				conn, err = server.DialProxy(net.JoinHostPort(host, strconv.Itoa(port)), nil)
				if err != nil {
					logPrintln(1, host, err)
					return
				}
			} else {
				var b [1500]byte
				n, err := client.Read(b[:])
				if err != nil {
					logPrintln(1, err)
					return
				}

				conn, err = server.DialProxy(net.JoinHostPort(host, strconv.Itoa(port)), b[:n])
				if err != nil {
					logPrintln(1, err)
					return
				}
			}
		} else if server.Option != 0 {
			if ips == nil {
				_, ips = NSLookup(host, server.Option, server.Server)
				if len(ips) == 0 {
					logPrintln(1, host, "no such host")
					return
				}
			}

			var b [1500]byte
			n, err := client.Read(b[:])
			if err != nil {
				logPrintln(1, err)
				return
			}

			if b[0] == 0x16 {
				offset, length := GetSNI(b[:n])
				if length > 0 {
					host = string(b[offset : offset+length])
					server = ConfigLookup(host)
				}

				logPrintln(1, "Redirect:", client.RemoteAddr(), "->", host, port, server)

				conn, err = server.Dial(ips, port, b[:n])
				if err != nil {
					logPrintln(1, host, err)
					return
				}
			} else {
				logPrintln(1, "Redirect:", client.RemoteAddr(), "->", host, port, server)
				if server.Option&OPT_HTTP3 != 0 {
					HttpMove(client, "h3", b[:n])
					return
				} else if server.Option&OPT_HTTPS != 0 {
					HttpMove(client, "https", b[:n])
					return
				} else if server.Option&OPT_MOVE != 0 {
					HttpMove(client, server.Server, b[:n])
					return
				} else if server.Option&OPT_STRIP != 0 {
					ip := ips[rand.Intn(len(ips))]
					if server.Option&OPT_FRONTING != 0 {
						host = ""
					}
					conn, err = DialStrip(ip.String(), host)
					if err != nil {
						logPrintln(1, err)
						return
					}
					_, err = conn.Write(b[:n])
					if err != nil {
						logPrintln(1, err)
						return
					}
				} else {
					conn, err = server.HTTP(client, ips, port, b[:n])
					if err != nil {
						logPrintln(1, err)
						return
					}
					io.Copy(client, conn)
					return
				}
			}
		} else if ips != nil {
			logPrintln(1, "RedirectProxy:", client.RemoteAddr(), "->", addr)
			conn, err = net.DialTCP("tcp", nil, addr)
			if err != nil {
				logPrintln(1, host, err)
				return
			}
		}
	}

	if conn == nil {
		return
	}

	defer conn.Close()

	_, _, err := relay(client, conn)
	if err != nil {
		if err, ok := err.(net.Error); ok && err.Timeout() {
			return // ignore i/o timeout
		}
		logPrintln(1, "relay error:", err)
	}
}

func QUICProxy(address string) {
	client, err := ListenUDP(address)
	if err != nil {
		logPrintln(1, err)
		return
	}
	defer client.Close()

	var UDPLock sync.Mutex
	var UDPMap map[string]net.Conn = make(map[string]net.Conn)
	data := make([]byte, 1500)

	for {
		n, clientAddr, err := client.ReadFromUDP(data)
		if err != nil {
			logPrintln(1, err)
			return
		}

		udpConn, ok := UDPMap[clientAddr.String()]

		if ok {
			udpConn.Write(data[:n])
		} else {
			SNI := GetQUICSNI(data[:n])
			if SNI != "" {
				server := ConfigLookup(SNI)
				if server.Option&OPT_UDP == 0 {
					continue
				}
				_, ips := NSLookup(SNI, server.Option, server.Server)
				if ips == nil {
					continue
				}

				logPrintln(1, "[QUIC]", clientAddr.String(), SNI, ips)

				udpConn, err = net.DialUDP("udp", nil, &net.UDPAddr{IP: ips[0], Port: 443})
				if err != nil {
					logPrintln(1, err)
					continue
				}

				if server.Option&OPT_ZERO != 0 {
					zero_data := make([]byte, 8+rand.Intn(1024))
					_, err = udpConn.Write(zero_data)
					if err != nil {
						logPrintln(1, err)
						continue
					}
				}

				UDPMap[clientAddr.String()] = udpConn
				_, err = udpConn.Write(data[:n])
				if err != nil {
					logPrintln(1, err)
					continue
				}

				go func(clientAddr net.UDPAddr) {
					data := make([]byte, 1500)
					udpConn.SetReadDeadline(time.Now().Add(time.Minute * 2))
					for {
						n, err := udpConn.Read(data)
						if err != nil {
							UDPLock.Lock()
							delete(UDPMap, clientAddr.String())
							UDPLock.Unlock()
							udpConn.Close()
							return
						}
						udpConn.SetReadDeadline(time.Now().Add(time.Minute * 2))
						client.WriteToUDP(data[:n], &clientAddr)
					}
				}(*clientAddr)
			}
		}
	}
}

func Socks4UProxy(address string) {
	laddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		logPrintln(1, err)
		return
	}
	client, err := net.ListenUDP("udp", laddr)
	if err != nil {
		logPrintln(1, err)
		return
	}
	defer client.Close()

	data := make([]byte, 1500)
	for {
		n, srcAddr, err := client.ReadFromUDP(data)
		if err != nil {
			logPrintln(1, err)
			continue
		}

		var host string
		var port int
		if n > 8 && data[0] == 4 && data[1] == 1 {
			userEnd := 8 + bytes.IndexByte(data[8:n], 0)
			port = int(binary.BigEndian.Uint16(data[2:4]))
			if data[4]|data[5]|data[6] == 0 {
				hostEnd := bytes.IndexByte(data[userEnd+1:n], 0)
				if hostEnd > 0 {
					host = string(data[userEnd+1 : userEnd+1+hostEnd])
				} else {
					client.WriteToUDP([]byte{0, 91, 0, 0, 0, 0, 0, 0}, srcAddr)
					continue
				}
			} else {
				if data[4] == VirtualAddrPrefix {
					index := int(binary.BigEndian.Uint32(data[6:8]))
					if index >= len(Nose) {
						return
					}
					host = Nose[index]
				} else {
					host = net.IP(data[4:8]).String()
				}
			}

			client.WriteToUDP([]byte{0, 90, data[2], data[3], data[4], data[5], data[6], data[7]}, srcAddr)
		} else {
			client.WriteToUDP([]byte{0, 91, 0, 0, 0, 0, 0, 0}, srcAddr)
			continue
		}

		server := ConfigLookup(host)
		if server.Option&(OPT_UDP|OPT_HTTP3) == 0 {
			continue
		}
		if server.Option&(OPT_HTTP3) != 0 {
			if GetQUICVersion(data[:n]) == 0 {
				continue
			}
		}

		logPrintln(1, "Socks4U:", srcAddr, "->", host, port, server)

		localConn, err := net.DialUDP("udp", nil, srcAddr)
		if err != nil {
			logPrintln(1, err)
			continue
		}

		var remoteConn net.Conn = nil
		var proxyConn net.Conn = nil
		if server.Proxy != "" {
			remoteAddress := net.JoinHostPort(host, strconv.Itoa(port))
			remoteConn, proxyConn, err = server.DialProxyUDP(remoteAddress)
		} else {
			_, ips := NSLookup(host, server.Option, server.Server)
			if ips == nil {
				localConn.Close()
				continue
			}

			raddr := net.UDPAddr{IP: ips[0], Port: port}
			remoteConn, err = net.DialUDP("udp", nil, &raddr)
		}

		if err != nil {
			logPrintln(1, err)
			localConn.Close()
			if proxyConn != nil {
				proxyConn.Close()
			}
			continue
		}

		if server.Option&OPT_ZERO != 0 {
			zero_data := make([]byte, 8+rand.Intn(1024))
			_, err = remoteConn.Write(zero_data)
			if err != nil {
				logPrintln(1, err)
				localConn.Close()
				if proxyConn != nil {
					proxyConn.Close()
				}
				continue
			}
		}

		_, err = remoteConn.Write(data[:n])
		if err != nil {
			logPrintln(1, err)
			localConn.Close()
			if proxyConn != nil {
				proxyConn.Close()
			}
			continue
		}

		go func(localConn, remoteConn, proxyConn net.Conn) {
			relayUDP(localConn, remoteConn)
			remoteConn.Close()
			localConn.Close()
			if proxyConn != nil {
				proxyConn.Close()
			}
		}(localConn, remoteConn, proxyConn)
	}
}
