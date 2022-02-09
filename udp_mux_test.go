//go:build !js
// +build !js

package ice

//nolint:gosec
import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/stun"
	"github.com/pion/transport/test"
	"github.com/stretchr/testify/require"
)

func TestUDPMux(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	conn, err := net.ListenUDP(udp, &net.UDPAddr{})
	require.NoError(t, err)

	udpMux := NewUDPMuxDefault(UDPMuxParams{
		Logger:  nil,
		UDPConn: conn,
	})

	require.NoError(t, err)

	defer func() {
		_ = udpMux.Close()
		_ = conn.Close()
	}()

	require.NotNil(t, udpMux.LocalAddr(), "tcpMux.LocalAddr() is nil")

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		testMuxConnection(t, udpMux, "ufrag1", udp)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		testMuxConnection(t, udpMux, "ufrag2", "udp4")
	}()

	// skip ipv6 test on i386
	const ptrSize = 32 << (^uintptr(0) >> 63)
	if ptrSize != 32 {
		testMuxConnection(t, udpMux, "ufrag3", "udp6")
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		testMuxSrflxConnection(t, udpMux, "ufrag4", udp)
	}()

	wg.Wait()

	require.NoError(t, udpMux.Close())

	// can't create more connections
	_, err = udpMux.GetConn("failufrag")
	require.Error(t, err)
}

func TestAddressEncoding(t *testing.T) {
	cases := []struct {
		name string
		addr net.UDPAddr
	}{
		{
			name: "empty address",
		},
		{
			name: "ipv4",
			addr: net.UDPAddr{
				IP:   net.IPv4(244, 120, 0, 5),
				Port: 6000,
				Zone: "",
			},
		},
		{
			name: "ipv6",
			addr: net.UDPAddr{
				IP:   net.IPv6loopback,
				Port: 2500,
				Zone: "zone",
			},
		},
	}

	for _, c := range cases {
		addr := c.addr
		t.Run(c.name, func(t *testing.T) {
			buf := make([]byte, maxAddrSize)
			n, err := encodeUDPAddr(&addr, buf)
			require.NoError(t, err)

			parsedAddr, err := decodeUDPAddr(buf[:n])
			require.NoError(t, err)
			require.EqualValues(t, &addr, parsedAddr)
		})
	}
}

func testMuxConnection(t *testing.T, udpMux *UDPMuxDefault, ufrag string, network string) {
	pktConn, err := udpMux.GetConn(ufrag)
	require.NoError(t, err, "error retrieving muxed connection for ufrag")
	defer func() {
		_ = pktConn.Close()
	}()

	remoteConn, err := net.DialUDP(network, nil, &net.UDPAddr{
		Port: udpMux.LocalAddr().(*net.UDPAddr).Port,
	})
	require.NoError(t, err, "error dialing test udp connection")

	// initial messages are dropped
	_, err = remoteConn.Write([]byte("dropped bytes"))
	require.NoError(t, err)
	// wait for packet to be consumed
	time.Sleep(time.Millisecond)

	// write out to establish connection
	msg := stun.New()
	msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
	msg.Add(stun.AttrUsername, []byte(ufrag+":otherufrag"))
	msg.Encode()
	_, err = pktConn.WriteTo(msg.Raw, remoteConn.LocalAddr())
	require.NoError(t, err)

	// ensure received
	buf := make([]byte, receiveMTU)
	n, err := remoteConn.Read(buf)
	require.NoError(t, err)
	require.Equal(t, msg.Raw, buf[:n])

	// start writing packets through mux
	targetSize := 1 * 1024 * 1024
	readDone := make(chan struct{}, 1)
	remoteReadDone := make(chan struct{}, 1)

	// read packets from the muxed side
	go func() {
		defer func() {
			t.Logf("closing read chan for: %s", ufrag)
			close(readDone)
		}()
		readBuf := make([]byte, receiveMTU)
		nextSeq := uint32(0)
		for read := 0; read < targetSize; {
			n, _, err := pktConn.ReadFrom(readBuf)
			require.NoError(t, err)
			require.Equal(t, receiveMTU, n)

			verifyPacket(t, readBuf[:n], nextSeq)

			// write it back to sender
			_, err = pktConn.WriteTo(readBuf[:n], remoteConn.LocalAddr())
			require.NoError(t, err)

			read += n
			nextSeq++
		}
	}()

	go func() {
		defer func() {
			close(remoteReadDone)
		}()
		readBuf := make([]byte, receiveMTU)
		nextSeq := uint32(0)
		for read := 0; read < targetSize; {
			n, _, err := remoteConn.ReadFrom(readBuf)
			require.NoError(t, err)
			require.Equal(t, receiveMTU, n)

			verifyPacket(t, readBuf[:n], nextSeq)

			read += n
			nextSeq++
		}
	}()

	sequence := 0
	for written := 0; written < targetSize; {
		buf := make([]byte, receiveMTU)
		// byte0-4: sequence
		// bytes4-24: sha1 checksum
		// bytes24-mtu: random data
		_, err := rand.Read(buf[24:])
		require.NoError(t, err)
		h := sha1.Sum(buf[24:]) //nolint:gosec
		copy(buf[4:24], h[:])
		binary.LittleEndian.PutUint32(buf[0:4], uint32(sequence))

		_, err = remoteConn.Write(buf)
		require.NoError(t, err)

		written += len(buf)
		sequence++

		time.Sleep(time.Millisecond)
	}

	<-readDone
	<-remoteReadDone
}

func testMuxSrflxConnection(t *testing.T, udpMux *UDPMuxDefault, ufrag string, network string) {
	pktConn, err := udpMux.GetConn(ufrag)
	require.NoError(t, err, "error retrieving muxed connection for ufrag")
	defer func() {
		_ = pktConn.Close()
	}()

	remoteConn, err := net.DialUDP(network, nil, &net.UDPAddr{
		Port: udpMux.LocalAddr().(*net.UDPAddr).Port,
	})
	require.NoError(t, err, "error dialing test udp connection")
	defer func() {
		_ = remoteConn.Close()
	}()

	// use small value for TTL to check expiration of the address
	udpMux.params.XORMappedAddrCacheTTL = time.Millisecond * 20
	testXORIP := net.ParseIP("213.141.156.236")
	testXORPort := 21254

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		address, err := udpMux.GetXORMappedAddr(remoteConn.LocalAddr(), time.Second)
		require.NoError(t, err)
		require.NotNil(t, address)
		require.True(t, address.IP.Equal(testXORIP))
		require.Equal(t, address.Port, testXORPort)
	}()

	// wait until GetXORMappedAddr calls sendStun method
	time.Sleep(time.Millisecond)

	// check that mapped address filled correctly after sent stun
	udpMux.mu.Lock()
	mappedAddr, ok := udpMux.xorMappedAddr[remoteConn.LocalAddr().String()]
	require.True(t, ok)
	require.NotNil(t, mappedAddr)
	require.True(t, mappedAddr.pending())
	require.False(t, mappedAddr.expired())
	udpMux.mu.Unlock()

	// clean receiver read buffer
	buf := make([]byte, receiveMTU)
	_, err = remoteConn.Read(buf)
	require.NoError(t, err)

	// write back to udpMux XOR message with address
	msg := stun.New()
	msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
	msg.Add(stun.AttrUsername, []byte(ufrag+":otherufrag"))
	addr := &stun.XORMappedAddress{
		IP:   testXORIP,
		Port: testXORPort,
	}
	addr.AddTo(msg)
	msg.Encode()
	_, err = remoteConn.Write(msg.Raw)
	require.NoError(t, err)

	// wait for the packet to be consumed and parsed by udpMux
	wg.Wait()

	// we should get address immediately from the cached map
	address, err := udpMux.GetXORMappedAddr(remoteConn.LocalAddr(), time.Second)
	require.NoError(t, err)
	require.NotNil(t, address)

	udpMux.mu.Lock()
	// check mappedAddr is not pending, we didn't send stun twice
	require.False(t, mappedAddr.pending())

	// check expiration by TTL
	time.Sleep(time.Millisecond * 21)
	require.True(t, mappedAddr.expired())
	udpMux.mu.Unlock()

	// after expire, we send stun request again
	// but we not receive response in 5 milliseconds and should get error here
	address, err = udpMux.GetXORMappedAddr(remoteConn.LocalAddr(), time.Millisecond*5)
	require.NotNil(t, err)
	require.Nil(t, address)
}

func verifyPacket(t *testing.T, b []byte, nextSeq uint32) {
	readSeq := binary.LittleEndian.Uint32(b[0:4])
	require.Equal(t, nextSeq, readSeq)
	h := sha1.Sum(b[24:]) //nolint:gosec
	require.Equal(t, h[:], b[4:24])
}

func TestUDPMux_Agent_Restart(t *testing.T) {
	oneSecond := time.Second
	connA, connB := pipe(&AgentConfig{
		DisconnectedTimeout: &oneSecond,
		FailedTimeout:       &oneSecond,
	})

	aNotifier, aConnected := onConnected()
	require.NoError(t, connA.agent.OnConnectionStateChange(aNotifier))

	bNotifier, bConnected := onConnected()
	require.NoError(t, connB.agent.OnConnectionStateChange(bNotifier))

	// Maintain Credentials across restarts
	ufragA, pwdA, err := connA.agent.GetLocalUserCredentials()
	require.NoError(t, err)

	ufragB, pwdB, err := connB.agent.GetLocalUserCredentials()
	require.NoError(t, err)

	require.NoError(t, err)

	// Restart and Re-Signal
	require.NoError(t, connA.agent.Restart(ufragA, pwdA))
	require.NoError(t, connB.agent.Restart(ufragB, pwdB))

	require.NoError(t, connA.agent.SetRemoteCredentials(ufragB, pwdB))
	require.NoError(t, connB.agent.SetRemoteCredentials(ufragA, pwdA))
	gatherAndExchangeCandidates(connA.agent, connB.agent)

	// Wait until both have gone back to connected
	<-aConnected
	<-bConnected

	require.NoError(t, connA.agent.Close())
	require.NoError(t, connB.agent.Close())
}
