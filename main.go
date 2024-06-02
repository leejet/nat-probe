// Some codes adapted from https://github.com/pion/stun/blob/master/cmd/stun-nat-behaviour/main.go,
// license: https://github.com/pion/stun/blob/master/LICENSE
package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/pion/stun"
)

var (
	stunServerAddr     string
	timeout            int
	verbose            bool
	bindAddr           string
	errResponseMessage = errors.New("error reading from response message channel")
	errTimedOut        = errors.New("timed out waiting for response")
)

func init() {
	flag.StringVar(&stunServerAddr, "s", "stun.syncthing.net:3478", "STUN server address")
	flag.BoolVar(&verbose, "v", false, "verbose")
	flag.IntVar(&timeout, "t", 3, "the number of seconds to wait for STUN server's response")
	flag.StringVar(&bindAddr, "b", "0.0.0.0", "bind UDP socket to a specific local address")
}

func logInfo(format string, a ...interface{}) {
	if verbose {
		fmt.Printf(format+"\n", a...)
	}
}

func logWarn(format string, a ...interface{}) {
	fmt.Printf(format+"\n", a...)
}

func logError(format string, a ...interface{}) {
	fmt.Printf(format+"\n", a...)
}

type STUNConn struct {
	conn        net.PacketConn
	messageChan chan *stun.Message
}

func (s *STUNConn) Close() error {
	return s.conn.Close()
}

func (s *STUNConn) Listen(bindUDPAddr *net.UDPAddr) error {
	conn, err := net.ListenUDP("udp4", bindUDPAddr)
	if err != nil {
		return err
	}
	s.conn = conn

	s.messageChan = make(chan *stun.Message)
	go func() {
		for {
			buf := make([]byte, 1024)

			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				close(s.messageChan)
				return
			}
			logInfo("Response from %v (%v bytes)", addr, n)
			buf = buf[:n]

			m := new(stun.Message)
			m.Raw = buf
			err = m.Decode()
			if err != nil {
				logInfo("Error decoding message: %v", err)
				close(s.messageChan)
				return
			}

			s.messageChan <- m
		}
	}()
	return nil
}

// Send request and wait for response or timeout
func (s *STUNConn) RoundTrip(msg *stun.Message, addr net.Addr) (*stun.Message, error) {
	_ = msg.NewTransactionID()
	logInfo("Sending to %v (%v bytes)", addr, len(msg.Raw))
	_, err := s.conn.WriteTo(msg.Raw, addr)
	if err != nil {
		logError("Error sending request to %v", addr)
		return nil, err
	}

	// Wait for response or timeout
	select {
	case m, ok := <-s.messageChan:
		if !ok {
			return nil, errResponseMessage
		}
		return m, nil
	case <-time.After(time.Duration(timeout) * time.Second):
		logInfo("Timed out waiting for response from server %v", addr)
		return nil, errTimedOut
	}
}

type SourceAddress struct {
	IP   net.IP
	Port int
}

func (s *SourceAddress) GetFrom(m *stun.Message) error {
	a := (*stun.MappedAddress)(s)
	return a.GetFromAs(m, stun.AttrSourceAddress)
}

type ChangedAddress struct {
	IP   net.IP
	Port int
}

func (c *ChangedAddress) GetFrom(m *stun.Message) error {
	a := (*stun.MappedAddress)(c)
	return a.GetFromAs(m, stun.AttrChangedAddress)
}

type STUNResponse struct {
	otherAddr  *stun.OtherAddress
	respOrigin *stun.ResponseOrigin
	mappedAddr *stun.MappedAddress
}

func (r *STUNResponse) GetFrom(msg *stun.Message) {
	xorAddr := &stun.XORMappedAddress{}
	if xorAddr.GetFrom(msg) == nil {
		r.mappedAddr = &stun.MappedAddress{}
		r.mappedAddr.IP = xorAddr.IP
		r.mappedAddr.Port = xorAddr.Port
	} else {
		mappedAddr := &stun.MappedAddress{}
		if mappedAddr.GetFrom(msg) == nil {
			r.mappedAddr = mappedAddr
		}
	}

	respOrigin := &stun.ResponseOrigin{}
	if respOrigin.GetFrom(msg) == nil {
		r.respOrigin = respOrigin
	} else {
		sourceAddr := &SourceAddress{}
		if sourceAddr.GetFrom(msg) == nil {
			r.respOrigin = &stun.ResponseOrigin{}
			r.respOrigin.IP = sourceAddr.IP
			r.respOrigin.Port = sourceAddr.Port
		}
	}

	otherAddr := &stun.OtherAddress{}
	if otherAddr.GetFrom(msg) == nil {
		r.otherAddr = otherAddr
	} else {
		changedAddr := &ChangedAddress{}
		if changedAddr.GetFrom(msg) == nil {
			r.otherAddr = &stun.OtherAddress{}
			r.otherAddr.IP = changedAddr.IP
			r.otherAddr.Port = changedAddr.Port
		}
	}
}

func (r *STUNResponse) String() string {
	s := "Mapped Address: "
	if r.mappedAddr != nil {
		s += r.mappedAddr.String()
	}
	s += ", Response Origin: "
	if r.respOrigin != nil {
		s += r.respOrigin.String()
	}
	s += ", Other Address: "
	if r.otherAddr != nil {
		s += r.otherAddr.String()
	}
	return s
}

func MappingBehaviorProbe(bindUDPAddr *net.UDPAddr, stunServerUDPAddr *net.UDPAddr) error {
	logInfo("Determining NAT Mapping Behavior")
	stunConn := &STUNConn{}
	err := stunConn.Listen(bindUDPAddr)
	if err != nil {
		return err
	}
	defer stunConn.Close()
	fmt.Printf("Local Address : %v\n", stunConn.conn.LocalAddr())

	// Test I: Regular binding request
	logInfo("------------------------------------------")
	logInfo("Test I: Regular binding request")
	req := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	msg, err := stunConn.RoundTrip(req, stunServerUDPAddr)
	if err != nil {
		return err
	}
	resp1 := &STUNResponse{}
	resp1.GetFrom(msg)
	logInfo("Parse %v", resp1)
	if resp1.mappedAddr == nil || resp1.otherAddr == nil {
		logError("NAT discovery feature not supported by this server")
		return errResponseMessage
	}

	otherSTUNServerUDPAddr, err := net.ResolveUDPAddr("udp4", resp1.otherAddr.String())
	if err != nil {
		logError("Error resolving other STUN server address: %s", err.Error())
		return err
	}
	if stunConn.conn.LocalAddr().String() == resp1.mappedAddr.String() {
		fmt.Printf("Mapped Address: %v\n", resp1.mappedAddr)
		fmt.Println("NAT Mapping Behavior: No NAT")
		return nil
	}
	// fmt.Printf("Local Address : %v\n", stunConn.conn.LocalAddr())

	// Test II: Send binding request to the other address but primary port
	logInfo("------------------------------------------")
	logInfo("Test II: Send binding request to the other address but primary port")
	otherAddr := *otherSTUNServerUDPAddr
	otherAddr.Port = stunServerUDPAddr.Port
	msg, err = stunConn.RoundTrip(req, &otherAddr)
	if err != nil {
		return err
	}
	resp2 := &STUNResponse{}
	resp2.GetFrom(msg)
	logInfo("Parse %v", resp2)
	if resp2.mappedAddr == nil {
		logError("NAT discovery feature not supported by this server")
		return errResponseMessage
	}

	if resp1.mappedAddr.String() == resp2.mappedAddr.String() {
		fmt.Printf("Mapped Address: %v\n", resp1.mappedAddr)
		fmt.Println("NAT Mapping Behavior: Endpoint Independent")
		return nil
	}

	// Test III: Send binding request to the other address and port
	logInfo("------------------------------------------")
	logInfo("Test III: Send binding request to the other address and port")
	msg, err = stunConn.RoundTrip(req, otherSTUNServerUDPAddr)
	if err != nil {
		return err
	}
	resp3 := &STUNResponse{}
	resp3.GetFrom(msg)
	logInfo("Parse %v", resp3)
	if resp3.mappedAddr == nil {
		logError("NAT discovery feature not supported by this server")
		return errResponseMessage
	}

	fmt.Printf("External IP: %v\n", resp1.mappedAddr.IP)
	if resp3.mappedAddr.String() == resp2.mappedAddr.String() {
		fmt.Println("NAT Mapping Behavior: Address Dependent")
	} else {
		fmt.Println("NAT Mapping Behavior: Address and Port Dependent")
	}

	return nil
}

func FilteringBehaviorProbe(bindUDPAddr *net.UDPAddr, stunServerUDPAddr *net.UDPAddr) error {
	logInfo("Determining NAT Filtering Behavior")
	stunConn := &STUNConn{}
	err := stunConn.Listen(bindUDPAddr)
	if err != nil {
		return err
	}
	defer stunConn.Close()
	// fmt.Printf("Local Address : %v\n", stunConn.conn.LocalAddr())

	// Test I: Regular binding request
	logInfo("------------------------------------------")
	logInfo("Test I: Regular binding request")
	req := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	msg, err := stunConn.RoundTrip(req, stunServerUDPAddr)
	if err != nil {
		return err
	}
	resp1 := &STUNResponse{}
	resp1.GetFrom(msg)
	logInfo("Parse %v", resp1)
	if resp1.mappedAddr == nil || resp1.otherAddr == nil {
		logError("NAT discovery feature not supported by this server")
		return errResponseMessage
	}

	// Test II: Request to change both IP and port
	logInfo("------------------------------------------")
	logInfo("Test II: Request to change both IP and port")
	req = stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	req.Add(stun.AttrChangeRequest, []byte{0x00, 0x00, 0x00, 0x06})
	msg, err = stunConn.RoundTrip(req, stunServerUDPAddr)
	if err == nil {
		resp2 := &STUNResponse{}
		resp2.GetFrom(msg)
		logInfo("Parse %v", resp2)
		fmt.Println("NAT Filtering Behavior: Endpoint Independent")
		return nil
	} else if !errors.Is(err, errTimedOut) {
		return err
	}

	// Test III: Request to change port only
	logInfo("------------------------------------------")
	logInfo("Test III: Request to change port only")
	req = stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	req.Add(stun.AttrChangeRequest, []byte{0x00, 0x00, 0x00, 0x02})
	msg, err = stunConn.RoundTrip(req, stunServerUDPAddr)
	if err == nil {
		resp3 := &STUNResponse{}
		resp3.GetFrom(msg)
		logInfo("Parse %v", resp3)
		fmt.Println("NAT Filtering Behavior: Address Dependent")
		return nil
	} else if !errors.Is(err, errTimedOut) {
		return err
	}
	fmt.Println("NAT Filtering Behavior: Address and Port Dependent")

	// Test HairPin
	req = stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	mappedUDPAddr := net.UDPAddr{
		IP:   resp1.mappedAddr.IP,
		Port: resp1.mappedAddr.Port,
	}
	msg, err = stunConn.RoundTrip(req, &mappedUDPAddr)
	if err == nil {
		resp4 := &STUNResponse{}
		resp4.GetFrom(msg)
		logInfo("Parse %v", resp4)
		fmt.Println("Haripin: True")
		return nil
	} else if !errors.Is(err, errTimedOut) {
		return err
	}
	fmt.Println("Haripin: False")

	return nil
}

func main() {
	flag.Parse()
	if !strings.Contains(bindAddr, ":") {
		bindAddr += ":0"
	}
	bindUDPAddr, err := net.ResolveUDPAddr("udp4", bindAddr)
	if err != nil {
		fmt.Printf("Error resolving bind address: %s", err.Error())
		return
	}
	logInfo("resolve bind address %s to %v", bindAddr, bindUDPAddr)
	logInfo("resolving STUN server address: %s", stunServerAddr)
	stunServerUDPAddr, err := net.ResolveUDPAddr("udp4", stunServerAddr)
	if err != nil {
		fmt.Printf("Error resolving STUN server address: %s", err.Error())
		return
	}
	logInfo("resolve STUN server address %s to %v", stunServerAddr, stunServerUDPAddr)
	logInfo("")
	MappingBehaviorProbe(bindUDPAddr, stunServerUDPAddr)
	logInfo("")
	FilteringBehaviorProbe(bindUDPAddr, stunServerUDPAddr)
}
