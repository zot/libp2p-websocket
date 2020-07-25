module github.com/zot/ipfs-p2p-websocket

go 1.13

replace github.com/zot/textcraft-packet => /home/bill/work/p2pmud2-projects/textcraft-packet

replace github.com/zot/textcraft-blockrequest => /home/bill/work/p2pmud2-projects/textcraft-packet

require (
	github.com/AndreasBriese/bbloom v0.0.0-20190825152654-46b345b51c96 // indirect
	github.com/fsnotify/fsnotify v1.4.9 // indirect
	github.com/golang/groupcache v0.0.0-20200121045136-8c9f03a8e57e // indirect
	github.com/gorilla/websocket v1.4.2
	github.com/hsanjuan/ipfs-lite v1.1.14
	github.com/ipfs/go-cid v0.0.6
	github.com/ipfs/go-ipfs-config v0.8.0
	github.com/ipfs/go-ipfs-files v0.0.8 // indirect
	github.com/ipfs/go-log v1.0.4
	github.com/ipfs/go-log/v2 v2.1.1
	github.com/libp2p/go-libp2p v0.9.6
	github.com/libp2p/go-libp2p-autonat v0.2.3
	github.com/libp2p/go-libp2p-connmgr v0.2.4
	github.com/libp2p/go-libp2p-core v0.5.7
	github.com/libp2p/go-libp2p-discovery v0.4.0
	github.com/libp2p/go-libp2p-kad-dht v0.8.2
	github.com/libp2p/go-libp2p-quic-transport v0.6.0 // indirect
	github.com/libp2p/go-libp2p-secio v0.2.2
	github.com/libp2p/go-libp2p-tls v0.1.3
	github.com/libp2p/go-nat v0.0.5
	github.com/multiformats/go-multiaddr v0.2.2
	github.com/pkg/browser v0.0.0-20180916011732-0a3d74bf9ce4
	github.com/smartystreets/goconvey v0.0.0-20190710185942-9d28bd7c0945 // indirect
	github.com/vmihailenco/msgpack/v5 v5.0.0-beta.1
	github.com/zot/textcraft-packet v0.0.0-00010101000000-000000000000
	golang.org/x/lint v0.0.0-20200302205851-738671d3881b // indirect
	golang.org/x/net v0.0.0-20200520182314-0ba52f642ac2 // indirect
	golang.org/x/sync v0.0.0-20200317015054-43a5402ce75a // indirect
	golang.org/x/sys v0.0.0-20200523222454-059865788121 // indirect
	golang.org/x/tools v0.0.0-20200522201501-cb1345f3a375 // indirect
	gopkg.in/yaml.v2 v2.2.5 // indirect
)
