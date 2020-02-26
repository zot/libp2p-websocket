# IPFS-P2P-WEBSOCKET

Relay p2p connections between browsers transcoding between sockets and websockets

This runs a simple websocket server and listens for websocket connections using a simple protocol:

## NOTE: THIS IS A WORK IN PROGRESS

# LICENSE AND COPYRIGHT
Copyright 2020 Bill Burdick. All rights reserved.
Use of this source code is governed by an MIT-style
license that can be found in the LICENSE file.

# Running

`./libp2p-connection -browser chat.html` will start the relay and pop the chat example in a browser

In development, you can use -files to point to the live html/js/css files you are editing. If you are in the src directory. For example, you should be able to use this command to pop out a development version of chat:
```
libp2p-connection -files html -files examples -browse chat.html
```

## Usage:
```
Usage of libp2p-connection:
  -addr string
        host address to listen on
  -browse string
        Browse a URL
  -files value
        add the contents of a directory to serve from /
  -key string
        specify peer key
  -listen value
        Adds a multiaddress to the listen list
  -nopeers
        clear the bootstrap peer list
  -peer value
        Adds a peer multiaddress to the bootstrap list
  -port int
        port to listen on (default 8888)
```

# libp2p-connection runs a websocket server on /ws/control
This allows a browser to control the relay using a very simple binary protocol.
When a connection closes, it cleans up all of its child connections.
The client and server exchange these command messages, with the first byte of each message identifying the command.

# CLIENT-TO-SERVER MESSAGES
 
```
  Listen:       [1][PROTOCOL: rest]              -- request a listener for a protocol
  Stop:         [2][PROTOCOL: rest]              -- stop listening on PORT
  Close:        [3][ID: 8]                       -- close a stream
  Data:         [4][ID: 8][data: rest]           -- write data to stream
  Peer Connect: [5][PROTOCOL: STR][PEERID: rest] -- connect to another peer
```

# SERVER-TO-CLIENT MESSAGES

```
  Listener Connection:     [1][ID: 8][PROTOCOL: rest] -- new listener connection with id ID
  Connection Closed:       [2][ID: 8]                 -- connection ID closed
  Data:                    [3][ID: 8][data: rest]     -- receive data from stream with id ID
  Listen Refused:          [4][PROTOCOL: rest]        -- could not listen on PORT
  Listener Closed:         [5][PROTOCOL: rest]        -- could not listen on PORT
  Peer Connection:         [6][ID: 8]                 -- connected to a peer with id ID
  Peer Connection Refused: [7][PEERID: rest]          -- connection to peer PEERID refused
```

# Building

## Prerequisites
**go**: you need [go](https://golang.org/) if you want to build the go part (which includes updating the default HTML files)

**esc:** `go get -u github.com/mjibson/esc` -- make sure this is on your path (it should go into $GOHOME/bin).

## Building the default webdir

The `build` file is (probably?) a posix shell script that uses esc to generate files.go by combining examples/* and html/* into a directory and creating a virtual file system out of which the HTTP server serves files.

## build-and-run

Go compiles so fast that, while I'm developing, I build the entire project everytime I run it:

```shell
cd src
./build && go build libp2p-connection.go protocol.go files.go && ./libp2p-connection -browse chat.html
```
This creates an updated files.go, compiles the project, and then runs the chat example.
