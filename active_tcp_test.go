package ice

import (
	"fmt"
	"net"
	"testing"

	"github.com/pion/logging"
	"github.com/stretchr/testify/require"
)

func TestActiveTCP(t *testing.T) {
	r := require.New(t)

	const port = 7686

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{
		Port: port,
	})
	r.NoError(err)
	defer func() {
		_ = listener.Close()
	}()

	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel.Set(logging.LogLevelTrace)

	tcpMux := NewTCPMuxDefault(TCPMuxParams{
		Listener:       listener,
		Logger:         loggerFactory.NewLogger("ice"),
		ReadBufferSize: 20,
	})

	defer func() {
		_ = tcpMux.Close()
	}()

	r.NotNil(tcpMux.LocalAddr(), "tcpMux.LocalAddr() is nil")
	fmt.Println(tcpMux.LocalAddr())

	passiveAgent, err := NewAgent(&AgentConfig{
		TCPMux:         tcpMux,
		CandidateTypes: []CandidateType{CandidateTypeHost},
		NetworkTypes:   []NetworkType{NetworkTypeTCP4},
		LoggerFactory:  loggerFactory,
		activeTCP:      false,
	})
	r.NoError(err)
	r.NotNil(passiveAgent)

	activeAgent, err := NewAgent(&AgentConfig{
		CandidateTypes: []CandidateType{CandidateTypeHost},
		NetworkTypes:   []NetworkType{NetworkTypeTCP4},
		LoggerFactory:  loggerFactory,
		activeTCP:      true,
	})
	r.NoError(err)
	r.NotNil(activeAgent)

	passiveAgentConn, activeAgenConn := connect(passiveAgent, activeAgent)
	r.NotNil(passiveAgentConn)
	r.NotNil(activeAgenConn)

	pair := passiveAgent.getSelectedPair()
	r.NotNil(pair)
	r.Equal(port, pair.Local.Port())

	// send a packet from mux
	data := []byte("hello world")
	_, err = passiveAgentConn.Write(data)
	r.NoError(err)

	buffer := make([]byte, 1024)
	n, err := activeAgenConn.Read(buffer)
	r.NoError(err)
	r.Equal(data, buffer[:n])

	// send a packet to mux
	data2 := []byte("hello world 2")
	_, err = activeAgenConn.Write(data2)
	r.NoError(err)

	n, err = passiveAgentConn.Read(buffer)
	r.NoError(err)
	r.Equal(data2, buffer[:n])

	//r.NoError(activeAgenConn.Close())
	//r.NoError(passiveAgentConn.Close())
	//r.NoError(tcpMux.Close())
}

func TestUDP(t *testing.T) {
	r := require.New(t)

	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel.Set(logging.LogLevelTrace)

	passiveAgent, err := NewAgent(&AgentConfig{
		CandidateTypes: []CandidateType{CandidateTypeHost},
		NetworkTypes:   []NetworkType{NetworkTypeUDP4},
		LoggerFactory:  loggerFactory,
	})
	r.NoError(err)
	r.NotNil(passiveAgent)

	activeAgent, err := NewAgent(&AgentConfig{
		CandidateTypes: []CandidateType{CandidateTypeHost},
		NetworkTypes:   []NetworkType{NetworkTypeUDP4},
		LoggerFactory:  loggerFactory,
	})
	r.NoError(err)
	r.NotNil(activeAgent)

	passiveAgentConn, activeAgenConn := connect(passiveAgent, activeAgent)
	r.NotNil(passiveAgentConn)
	r.NotNil(activeAgenConn)
}
