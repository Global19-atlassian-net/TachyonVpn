package tachyonVpnClient

import (
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/tachyon-protocol/udw/udwBinary"
	"github.com/tachyon-protocol/udw/udwBytes"
	"github.com/tachyon-protocol/udw/udwConsole"
	"github.com/tachyon-protocol/udw/udwErr"
	"github.com/tachyon-protocol/udw/udwIo"
	"github.com/tachyon-protocol/udw/udwIpPacket"
	"github.com/tachyon-protocol/udw/udwLog"
	"github.com/tachyon-protocol/udw/udwNet"
	"github.com/tachyon-protocol/udw/udwNet/udwIPNet"
	"github.com/tachyon-protocol/udw/udwNet/udwTapTun"
	"github.com/tachyon-protocol/udw/udwRand"
	"io"
	"net"
	"strconv"
	"sync"
	"tachyonVpnProtocol"
)

type ClientRunReq struct {
	IsRelay      bool
	ServerIp     string
	ExitClientId uint64
}

func ClientRun(req ClientRunReq) {
	clientId := tachyonVpnProtocol.GetClientId()
	fmt.Println("ClientId:", clientId)
	tun, err := clientCreateTun(req.ServerIp)
	udwErr.PanicIfError(err)
	vpnConn, err := net.Dial("tcp", req.ServerIp+":"+strconv.Itoa(tachyonVpnProtocol.VpnPort))
	udwErr.PanicIfError(err)
	vpnConn = tls.Client(vpnConn, &tls.Config{
		ServerName:         udwRand.MustCryptoRandToReadableAlpha(5) + ".com",
		InsecureSkipVerify: true,
		NextProtos:         []string{"http/1.1", "h2"},
	})
	//TODO handshake with Server
	serverType := "DIRECT"
	if req.IsRelay {
		serverType = "RELAY"
		var (
			connRelaySide, plain = tachyonVpnProtocol.NewInternalConnectionDual()
			relayConn            = vpnConn
		)
		vpnConn = tls.Client(plain, &tls.Config{
			ServerName:         udwRand.MustCryptoRandToReadableAlpha(5) + ".com",
			InsecureSkipVerify: true,
			NextProtos:         []string{"http/1.1", "h2"},
		})
		go func() {
			var (
				buf       = udwBytes.NewBufWriter(nil)
				vpnPacket = &tachyonVpnProtocol.VpnPacket{}
			)
			for {
				buf.Reset()
				err := udwBinary.ReadByteSliceWithUint32LenToBufW(relayConn, buf)
				udwErr.PanicIfError(err)
				err = vpnPacket.Decode(buf.GetBytes())
				udwErr.PanicIfError(err)
				if vpnPacket.Cmd == tachyonVpnProtocol.CmdForward {
					if tachyonVpnProtocol.Debug {
						fmt.Println("read from relayConn", vpnPacket.ClientIdSender, "->", vpnPacket.ClientIdReceiver)
					}
					_, err := connRelaySide.Write(vpnPacket.Data)
					if err != nil {
						udwLog.Log("[8gys171bvm]", err)
					}
				} else {
					fmt.Println("[a3t7vfh1ms] Unexpected Cmd[", vpnPacket.Cmd, "]")
				}
			}
		}()
		go func() {
			vpnPacket := &tachyonVpnProtocol.VpnPacket{
				Cmd:              tachyonVpnProtocol.CmdForward,
				ClientIdSender:   clientId,
				ClientIdReceiver: req.ExitClientId,
			}
			buf := make([]byte, 10<<20)
			bufW := udwBytes.NewBufWriter(nil)
			for {
				n, err := connRelaySide.Read(buf)
				if err != nil {
					udwLog.Log("[e9erq1bwd1] close conn", err)
					_ = connRelaySide.Close()
					return
				}
				if tachyonVpnProtocol.Debug {
					fmt.Println("write to relayConn", vpnPacket.ClientIdSender, "->", vpnPacket.ClientIdReceiver)
				}
				vpnPacket.Data = buf[:n]
				bufW.Reset()
				vpnPacket.Encode(bufW)
				err = udwBinary.WriteByteSliceWithUint32LenNoAllocV2(relayConn, bufW.GetBytes())
				if err != nil {
					udwLog.Log("[n2cvu3w1cb]", err)
					continue
				}
			}
		}()
	}
	fmt.Println("Connected to", serverType, "Server ✔")
	go func() {
		vpnPacket := &tachyonVpnProtocol.VpnPacket{
			Cmd              : tachyonVpnProtocol.CmdData,
			ClientIdSender   : clientId,
			ClientIdReceiver : req.ExitClientId,
		}
		buf := make([]byte, 10<<20)
		bufW := udwBytes.NewBufWriter(nil)
		for {
			n, err := tun.Read(buf)
			udwErr.PanicIfError(err)
			vpnPacket.Data = buf[:n]
			bufW.Reset()
			vpnPacket.Encode(bufW)
			err = udwBinary.WriteByteSliceWithUint32LenNoAllocV2(vpnConn, bufW.GetBytes())
			udwErr.PanicIfError(err)
		}
	}()
	go func() {
		vpnPacket := &tachyonVpnProtocol.VpnPacket{}
		buf := udwBytes.NewBufWriter(nil)
		for {
			buf.Reset()
			err := udwBinary.ReadByteSliceWithUint32LenToBufW(vpnConn, buf)
			udwErr.PanicIfError(err)
			err = vpnPacket.Decode(buf.GetBytes())
			udwErr.PanicIfError(err)
			ipPacket, errMsg := udwIpPacket.NewIpv4PacketFromBuf(vpnPacket.Data)
			if errMsg != "" {
				panic("[zdy1mx9y3h]" + errMsg)
			}
			_, err = tun.Write(ipPacket.SerializeToBuf())
			if err != nil {
				udwLog.Log("[wmw12fyr9e] TUN Write error", err)
			}
		}
	}()
	udwConsole.WaitForExit()
}

func clientCreateTun(vpnServerIp string) (tun io.ReadWriteCloser, err error) {
	vpnClientIp := net.ParseIP("172.21.0.1")
	includeIpNetSet := udwIPNet.NewAllPassIpv4Net()
	includeIpNetSet.RemoveIpString(vpnServerIp)
	tunCreateCtx := &udwTapTun.CreateIpv4TunContext{
		SrcIp:        vpnClientIp,
		DstIp:        vpnClientIp,
		FirstIp:      vpnClientIp,
		DhcpServerIp: vpnClientIp,
		Mtu:          tachyonVpnProtocol.Mtu,
		Mask:         net.CIDRMask(30, 32),
	}
	err = udwTapTun.CreateIpv4Tun(tunCreateCtx)
	if err != nil {
		return nil, errors.New("[3xa38g7vtd] " + err.Error())
	}
	tunNamed := tunCreateCtx.ReturnTun
	vpnGatewayIp := vpnClientIp
	err = udwErr.PanicToError(func() {
		configLocalNetwork()
		ctx := udwNet.NewRouteContext()
		for _, ipNet := range includeIpNetSet.GetIpv4NetList() {
			goIpNet := ipNet.ToGoIPNet()
			ctx.MustRouteSet(*goIpNet, vpnGatewayIp)
		}
	})
	if err != nil {
		_ = tunNamed.Close()
		return nil, errors.New("[r8y8d5ash4] " + err.Error())
	}
	var closeOnce sync.Once
	return udwIo.StructWriterReaderCloser{
		Reader: tunNamed,
		Writer: tunNamed,
		Closer: udwIo.CloserFunc(func() error {
			closeOnce.Do(func() {
				_ = tunNamed.Close()
				err := udwErr.PanicToError(func() {
					recoverLocalNetwork()
				})
				if err != nil {
					udwLog.Log("error", "uninstallAllPassRoute", err.Error())
				}
			})
			return nil
		}),
	}, nil
}