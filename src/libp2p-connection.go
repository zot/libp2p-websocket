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
	"context"
	"flag"
	"fmt"
	"net/http"
	"path/filepath"
	"log"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
	"strings"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/libp2p/go-libp2p-discovery"
	autonat "github.com/libp2p/go-libp2p-autonat"
	//pubsub "github.com/libp2p/go-libp2p-pubsub"
	//pb "github.com/libp2p/go-libp2p-pubsub/pb"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	multiaddr "github.com/multiformats/go-multiaddr"
	logging "github.com/whyrusleeping/go-logging"

	goLog "github.com/ipfs/go-log"
	//"github.com/mr-tron/base58/base58"
)

/*
 * Parts of this were taken from Juan Batiz-Benet's libp2p chat example,
 * https://github.com/libp2p/go-libp2p-examples/blob/master/chat
 * and are Copyright (c) 2014 Juan Batiz-Benet, also licensed with the MIT license
 *
 */

const (
	rendezvousString = "p2pmud"
)

type addrList []multiaddr.Multiaddr

type libp2pRelay struct {
	relay
	clients map[*client]*libp2pClient
	host host.Host
}

type libp2pClient struct {
	client
	listeners map[string]*listener           // protocol -> listener
	listenerConnections map[uint64]*listener // connectionID -> listener
	forwarders map[uint64]*connection        // connectionID -> forwarder
	relay *libp2pRelay
}

type listener struct {
	client *libp2pClient                     // the client that owns this listener
	connections map[uint64]*connection       // connectionID -> connection
	protocol string
	frames bool                              // whether to transmit frame lengths
	managementChan chan func()               // client management
//	closing func(*listerner)                 // callback
}

var logger = goLog.Logger("p2pmud")
var peerKey string
var listenAddresses addrList
var bootstrapPeers addrList

func createLibp2pRelay() *libp2pRelay {
	r := new(libp2pRelay)
	(&r.relay).init()
	r.relay.handler = r
	r.clients = make(map[*client]*libp2pClient)
	return r
}

func (r *libp2pRelay) CreateClient() *client {
	c := new(libp2pClient)
	c.client.init(&r.relay)
	c.listeners = make(map[string]*listener)
	c.listenerConnections = make(map[uint64]*listener)
	c.forwarders = make(map[uint64]*connection)
	c.relay = r
	r.clients[&c.client] = c
	return &c.client
}

// LISTEN API METHOD
func (r *libp2pRelay) Listen(c *client, prot string, frames bool) {
	lis := createListener()
	lis.frames = frames
	lis.protocol = prot
	r.libp2pClient(c).listeners[prot] = lis
	lis.client = r.libp2pClient(c)
	fmt.Println("listen, protocol: ", prot, ", frames: ", frames)
	r.host.SetStreamHandler(protocol.ID(prot), func(stream network.Stream) {
		fmt.Println("GOT A CONNECTION")
		svc(c, func() {
			conID := c.newConnectionID()
			con := createConnection(prot, conID, stream, c, frames)
			fmt.Println("CONNECTION: ", con)
			lis.connections[conID] = con
			r.libp2pClient(c).listenerConnections[conID] = lis
			c.read(con)
		})
	})
}

// STOP LISTENER API METHOD
func (r *libp2pRelay) Stop(c *client, protocol string) {
	listener := r.libp2pClient(c).listeners[protocol]
	if listener != nil {
		listener.close(r.libp2pClient(c))
	}
}

func (r *libp2pRelay) HasConnection(c *client, id uint64) bool {
	return r.libp2pClient(c).hasConnection(id)
}

// CLOSE STREAM API METHOD
func (r *libp2pRelay) Close(c *client, id uint64) {
	lis := r.libp2pClient(c).listenerConnections[id]
	if lis != nil {
		lis.closeConnection(id)
	}
	fwd := r.libp2pClient(c).forwarders[id]
	if fwd != nil {
		fwd.close(func() {
			delete(r.libp2pClient(c).forwarders, id)
		})
	}
}

// SEND DATA API METHOD
func (r *libp2pRelay) Data(c *client, id uint64, data []byte) {
	var con *connection

	lis := r.libp2pClient(c).listenerConnections[id]
	if lis != nil {
		con = lis.connections[id]
	} else {
		con = r.libp2pClient(c).forwarders[id]
	}
	if con != nil {
		con.writeData(&r.relay, data)
	}
}

// CONNECT API METHOD
func (r *libp2pRelay) Connect(c *client, prot string, peerid string, frames bool) {
	fmt.Printf("Attempting to connect with protocol %v to peer %v\n", prot, peerid)
	pid, err := peer.Decode(peerid)
	if err != nil {
		c.connectionRefused(err, peerid, prot)
	}
	stream, err := r.host.NewStream(context.Background(), pid, protocol.ID(prot))
	if err != nil {
		c.connectionRefused(err, peerid, prot)
	} else {
		fmt.Println("Got connection")
		c.newConnection(prot, func(conID uint64) *connection {
			fwd := new(connection)
			fwd.initConnection("forwarder", prot, conID, stream, c, frames)
			r.libp2pClient(c).forwarders[conID] = fwd
			return fwd
		})
	}
}

func (r *libp2pRelay) libp2pClient(c *client) *libp2pClient {
	return r.clients[c]
}

func createListener() *listener {
	lis := new(listener)
	lis.connections = make(map[uint64]*connection)
	lis.managementChan = make(chan func())
	return lis
}

func (l *listener) close(c *libp2pClient) {
	c.relay.host.RemoveStreamHandler(protocol.ID(l.protocol))
	for id := range l.connections {
		l.closeConnection(id)
	}
	l.client.writeMessage(smsgListenerClosed, []byte(l.protocol))
	delete(l.client.listeners, l.protocol)
}

func (l *listener) closeConnection(id uint64) {
	fmt.Println("CLOSING SERVICE CONNECTION ", id)
	l.connections[id].close(func() {
		svc(l.client, func() {
			delete(l.client.listenerConnections, id)
		})
	})
	delete(l.connections, id)
}

func (c *libp2pClient) hasConnection(conID uint64) bool {
	return c.listenerConnections[conID] != nil || c.forwarders[conID] != nil
}

func checkErrWithMsg(err error, msg string) {
	if err != nil {
		fmt.Println(msg)
		panic(err)
	}
}

func checkErr(err error) {
	if err != nil {
		panic(err)
	}
}
func initp2p(relay *libp2pRelay) {
	goLog.SetAllLoggers(logging.WARNING)
	goLog.SetLogLevel("rendezvous", "info")
	ctx := context.Background()
	opts := []libp2p.Option{
		libp2p.NATPortMap(),
		//libp2p.EnableRelay(),
		//libp2p.EnableAutoRelay(),
		//libp2p.AddressFactory(func(addrs []ma.Multiaddr) []multiaddr.Multiaddr {
		//  return append(addrs, multiaddr.StringCast(bsaddr.Encapsulate(multiaddr.StringCast("/p2p-circuit"))))
		//}),
		libp2p.ListenAddrs([]multiaddr.Multiaddr(listenAddresses)...),
	}
	if peerKey != "" {
		keyBytes, err := crypto.ConfigDecodeKey(peerKey)
		checkErr(err)
		key, err := crypto.UnmarshalPrivateKey(keyBytes)
		opts = append(opts, libp2p.Identity(key))
	}
	// libp2p.New constructs a new libp2p Host. Other options can be added
	// here.
	myHost, err := libp2p.New(ctx, opts...)
	checkErr(err)
	logger.Info("Host created. We are:", myHost.ID())
	logger.Info(myHost.Addrs())
	relay.peerID = myHost.ID().Pretty()
	relay.host = myHost

	key := myHost.Peerstore().PrivKey(myHost.ID())
	keyBytes, err := crypto.MarshalPrivateKey(key)
	checkErr(err)
	keyString := crypto.ConfigEncodeKey(keyBytes)
	fmt.Printf("host private %s key: %s\n", reflect.TypeOf(key), keyString)

	// Start a DHT, for use in peer discovery. We can't just make a new DHT
	// client because we want each peer to maintain its own local copy of the
	// DHT, so that the bootstrapping node of the DHT can go down without
	// inhibiting future peer discovery.
	kademliaDHT, err := dht.New(ctx, myHost)
	checkErr(err)

	// Bootstrap the DHT. In the default configuration, this spawns a Background
	// thread that will refresh the peer table every five minutes.
	logger.Debug("Bootstrapping the DHT")
	checkErr(kademliaDHT.Bootstrap(ctx))

	// Let's connect to the bootstrap nodes first. They will tell us about the
	// other nodes in the network.
	var wg sync.WaitGroup
	var remaining int32 = int32(len(bootstrapPeers))
	fmt.Printf("@@@ WAITING FOR %d bootstrap peer connections...\n", remaining)
	for _, peerAddr := range bootstrapPeers {
		peerinfo, err := peer.AddrInfoFromP2pAddr(peerAddr)
		if err != nil {
			log.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := myHost.Connect(ctx, *peerinfo); err != nil {
				logger.Warning(err)
			} else {
				logger.Info("Connection established with bootstrap node:", *peerinfo)
			}
			rem := atomic.AddInt32(&remaining, -1)
			fmt.Printf("@@@ WAITING FOR %d bootstrap peer connections...\n", rem)
		}()
	}
	wg.Wait()

	// We use a rendezvous point "meet me here" to announce our location.
	// This is like telling your friends to meet you at the Eiffel Tower.
	logger.Info("Announcing ourselves...")
	routingDiscovery := discovery.NewRoutingDiscovery(kademliaDHT)
	discovery.Advertise(ctx, routingDiscovery, rendezvousString)
	logger.Debug("Successfully announced!")

	// Now, look for others who have announced
	// This is like your friend telling you the location to meet you.
	logger.Debug("Searching for other peers...")
	//peerChan, err := routingDiscovery.FindPeers(ctx, config.RendezvousString)
	_, err = routingDiscovery.FindPeers(ctx, rendezvousString)
	checkErr(err)

	/// MONITOR NAT STATUS
	fmt.Println("Creating autonat")
	an := autonat.NewAutoNAT(ctx, myHost, nil)
	oldStatus := -1
	go func() {
		for {
			status := an.Status()
			if int(status) != oldStatus {
				switch status {
				case autonat.NATStatusUnknown:
					fmt.Println("@@@ NAT status UNKNOWN")
				case autonat.NATStatusPublic:
					fmt.Println("@@@ NAT status PUBLIC")
				case autonat.NATStatusPrivate:
					fmt.Println("@@@ NAT status PRIVATE")
				}
			}
			oldStatus = int(status)
			time.Sleep(250 * time.Millisecond)
		}
	}()
}

func (al *addrList) String() string {
	strs := make([]string, len(*al))
	for i, addr := range *al {
		strs[i] = addr.String()
	}
	return strings.Join(strs, ",")
}

func (al *addrList) Set(value string) error {
	addr, err := multiaddr.NewMultiaddr(value)
	if err != nil {
		return err
	}
	*al = append(*al, addr)
	return nil
}

func StringsToAddrs(addrStrings []string) (maddrs []multiaddr.Multiaddr, err error) {
	for _, addrString := range addrStrings {
		addr, err := multiaddr.NewMultiaddr(addrString)
		if err != nil {
			return maddrs, err
		}
		maddrs = append(maddrs, addr)
	}
	return
}

func main() {
	relay := createLibp2pRelay()
	port := 8888
	files := ""
	flag.StringVar(&peerKey, "key", "", "specify peer key")
	flag.StringVar(&files, "files", files, "optional directory to use for file serving")
	flag.IntVar(&port, "port", port, "port to listen on")
	flag.Var(&bootstrapPeers, "peer", "Adds a peer multiaddress to the bootstrap list")
	flag.Var(&listenAddresses, "listen", "Adds a multiaddress to the listen list")
	flag.Parse()
	initp2p(relay)
	fmt.Println("Addresses:")
	for _, addr := range relay.host.Addrs() {
		fmt.Println("   ", addr.String()+"/p2p/"+relay.peerID)
	}
	fmt.Println("FINISHED INITIALIZING P2P, CREATING RELAY")
	runSvc(relay)
	fmt.Printf("Listening on port %v\nPeer id: %v\n", port, relay.peerID)
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
