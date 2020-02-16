/* Copyright (c) 2020, William R. Burdick Jr.
 *
 * The MIT License (MIT)
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
 * THE SOFTWARE.
 *
 */

package main

import (
	//"github.com/libp2p/go-libp2p"
	//"encoding/binary"
	"flag"
	"fmt"
	//"github.com/gorilla/websocket"
	"log"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	//"runtime/debug"
	"math/rand"
	//"time"
)

const ipfsName = "ipfs"

type ipfsRelay struct {
	relay
	clients map[*client]*ipfsClient
}

type ipfsClient struct {
	client
	listeners map[string]*listener           // protocol -> listener
	listenerConnections map[uint64]*listener // connectionID -> listener
	forwarders map[uint64]*forwarder         // connectionID -> forwarder
}

type clientCloser interface {
	close(*ipfsClient)
}

type forwarder struct {
	connection
	port int           // a record of the forwarder's port so we can close the IPFS forwarding service
}

type listener struct {
	listener *net.TCPListener                // socket listener
	client *ipfsClient                       // the client that owns this listener
	connections map[uint64]*connection       // connectionID -> connection
	port int
	protocol string
	frames bool                              // whether to transmit frame lengths
	managementChan chan func()               // client management
	closing func(*listener)                  // callback
}

var ipfsPath string

func createIpfsRelay() *ipfsRelay {
	r := new(ipfsRelay)
	r.clients = make(map[*client]*ipfsClient)
	(&r.relay).init()
	return r
}

func (r *ipfsRelay) ipfsClient(c *client) *ipfsClient {
	return r.clients[c]
}

func (r *ipfsRelay) CreateClient() *client {
	c := new(ipfsClient)
	c.client.init(&r.relay)
	c.listeners = make(map[string]*listener)
	c.listenerConnections = make(map[uint64]*listener)
	c.forwarders = make(map[uint64]*forwarder)
	r.clients[&c.client] = c
	return &c.client
}

// LISTEN API METHOD
func (r *ipfsRelay) Listen(c *client, protocol string, frames bool) {
	var port int
	var sock *net.TCPListener
	var err error

	attempts := 0
	for {
		port = (rand.Int() % (maxPort - minPort)) + minPort
		sock, err = net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IP{127,0,0,1}, Port: port})
		if err == nil {
			break
		}
		attempts++
		if attempts > 1000 {
			log.Fatal("No available ports!")
		}
	}
	err = cmd(ipfsPath, "p2p", "listen", protocol, "/ip4/127.0.0.1/tcp/"+strconv.Itoa(port)).Run()
	if err != nil {
		fmt.Println(err.Error())
		c.writePortMessage(smsgListenRefused, port)
		sock.Close()
	} else {
		lis := createListener()
		lis.frames = frames
		lis.port = port
		lis.listener = sock
		r.ipfsClient(c).listeners[protocol] = lis
		lis.run(r.ipfsClient(c))
	}
}

// STOP LISTENER API METHOD
func (r *ipfsRelay) Stop(c *client, protocol string) {
	listener := r.ipfsClient(c).listeners[protocol]
	if listener != nil {
		listener.close(r.ipfsClient(c))
	}
}

func (r *ipfsRelay) HasConnection(c *client, id uint64) bool {
	return r.ipfsClient(c).hasConnection(id)
}

// CLOSE STREAM API METHOD
func (r *ipfsRelay) Close(c *client, id uint64) {
	lis := r.ipfsClient(c).listenerConnections[id]
	if lis != nil {
		lis.closeConnection(id)
	}
	fwd := r.ipfsClient(c).forwarders[id]
	if fwd != nil {
		fwd.close(r.ipfsClient(c))
	}
}

// SEND DATA API METHOD
func (r *ipfsRelay) Data(c *client, id uint64, data []byte) {
	var con *connection

	lis := r.ipfsClient(c).listenerConnections[id]
	if lis != nil {
		con = lis.connections[id]
	}
	fwd := r.ipfsClient(c).forwarders[id]
	if fwd != nil {
		con = &fwd.connection
	}
	if con != nil {
		con.writeData(&r.relay, data)
	}
}

// CONNECT API METHOD
func (r *ipfsRelay) Connect(c *client, protocol string, peerid string, frames bool) {
	var con *net.TCPConn

	port := choosePort()
	err := cmd(ipfsPath, "p2p", "forward", protocol, "/ip4/127.0.0.1/tcp/"+strconv.Itoa(port), "/p2p/"+peerid).Run()
	if err == nil {
		con, err = net.DialTCP("tcp4", &net.TCPAddr{}, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	}
	if err != nil {
		c.connectionRefused(err, peerid, protocol)
	} else {
		initTcpConnection(con)
		c.newConnection(protocol, func(conID uint64) *connection {
			fwd := new(forwarder)
			fwd.initConnection("forwarder", protocol, conID, con, c, frames)
			fwd.port = port
			r.ipfsClient(c).forwarders[conID] = fwd
			return &fwd.connection
		})
	}
}

func (c *ipfsClient) hasConnection(conID uint64) bool {
	return c.listenerConnections[conID] != nil || c.forwarders[conID] != nil
}

func (c *ipfsClient) close() {
	c.client.close()
	c.running = false
	closable := make(map[int]clientCloser) // collect into this map because Close() alters c's maps
	for _, v := range c.listeners {
		closable[len(closable)] = v
	}
	for _, v := range c.forwarders {
		closable[len(closable)] = v
	}
	for _, cl := range closable {
		cl.close(c)
	}
}

func (f *forwarder) close(c *ipfsClient) {
	f.connection.close(func() {
		svc(f.client, func() {
			delete(c.forwarders, f.id)
		})
	})
}

func createListener() *listener {
	l := new(listener)
	l.connections = make(map[uint64]*connection)
	l.managementChan = make(chan func())
	return l
}

func (l *listener) getSvcChannel() chan func() {
	return l.managementChan
}


// process connections as they come in
func (l *listener) run(c *ipfsClient) {
	go func() {
		var tcpCon *net.TCPConn
		var err error = nil

		for err != nil {
			tcpCon, err = l.listener.AcceptTCP()
			svc(c, func() {
				if err != nil {
					l.close(c)
				} else {
					initTcpConnection(tcpCon)
					conID := c.newConnectionID()
					con := createConnection(l.protocol, conID, tcpCon, &c.client, l.frames)
					fmt.Println("CONNECTION: ", con)
					l.connections[conID] = con
					c.listenerConnections[conID] = l
					c.read(con)
				}
			})
		}
	}()
}

func initTcpConnection(tcpCon *net.TCPConn) {
	tcpCon.SetReadBuffer(maxMessageSize)
	tcpCon.SetWriteBuffer(maxMessageSize)
}

func (l *listener) close(c *ipfsClient) {
	l.listener.Close()
	for id := range l.connections {
		l.closeConnection(id)
	}
	cmd(ipfsPath, "p2p", "close", "-l", strconv.Itoa(l.port)).Run()
	l.client.writeMessage(smsgListenerClosed, []byte(l.protocol))
	delete(l.client.listeners, l.protocol)
}

func (l *listener) closeConnection(id uint64) {
	fmt.Println("CLOSING SERVICE CONNECTION ", id)
	delete(l.connections, id)
	l.connections[id].close(func() {
		svc(l.client, func() {
			delete(l.client.listenerConnections, id)
		})
	})
}

func cmd(prog string, args ...string) *exec.Cmd {
	str := [...]interface{}{"COMMAND: ", prog}
	iargs := make([]interface{}, len(args))
	for k, v := range args {
		iargs[k] = v
	}
	fmt.Println(append(str[:], iargs...)...)
	return exec.Command(ipfsPath, args...)
}

func runIpfsCmd(args ...string) string {
    cmd := cmd(ipfsPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("Error running ipfs %#v\n%v\n", args, err)
	}
	return string(output)
}

func main() {
	path, err := exec.LookPath("ipfs")
	if err != nil {
		log.Fatal(err)
	}
	ipfsPath = path
	relay := createIpfsRelay()
	relay.peerID = strings.Trim(runIpfsCmd("id", "-f", "<id>\n"), " \n")
	runSvc(relay)
	port := 8888
	files := ""
	flag.StringVar(&files, "files", "", "optional directory to use for file serving")
	flag.IntVar(&port, "port", 8888, "port to listen on")
	flag.Parse()
	fmt.Printf("Peer id: %v\nListening on port %v\n", relay.peerID, port)
	http.HandleFunc("/ipfswsrelay", relay.handleConnection())
	if files != "" {
		f, err := filepath.Abs(files)
		if err != nil {
			log.Fatal(err)
		}
		files = f
		if files != "" {
			http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				reqFile, err := filepath.Abs(filepath.Join(files, r.URL.Path))
				if err != nil || len(reqFile) < len(files) || files[:] != reqFile[0:len(files)] {
					http.Error(w, "Not found", http.StatusNotFound)
				} else {
					http.ServeFile(w, r, reqFile)
				}
			})
		}
	}
	log.Fatal(http.ListenAndServe(fmt.Sprintf("localhost:%d", port), nil))
}
