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

/*
 * Basic operation
 *
 * A public peer can host directly
 * For a private peer to host, it must collaborate with a public peer
 *
 */

import libp2p from "./protocol.js"

var {
    natStatus,
    RelayClient,
    RelayHost,
    RelayPeer,
    RelayService,
    CommandHandler,
    TrackingHandler,
    LoggingHandler,
    encode_ascii85,
    decode_ascii85,
    getConnectionInfo,
    close,
    sendObject,
    stop,
    listen,
    connect,
    getInfoForPeerAndProtocol,
} = libp2p;

/// simplementation of jQuery
function $(sel) {
    return typeof sel == 'string' ? document.querySelector(sel) : sel;
}
var $all = document.querySelectorAll.bind(document);
function $find(el, sel) {
    if (typeof el == 'string') {
        el = $all(el);
    }
    if (el instanceof NodeList) {
        el = [...el];
    }
    if (Array.isArray(el)) {
        for (var node of el) {
            var result = node.querySelector(sel);

            if (result) return result;
        }
    } else {
        return $(el).querySelector(sel);
    }
}
function $findAll(el, sel) {
    if (typeof el == 'string') {
        el = $all(el);
    }
    if (el instanceof NodeList) {
        el = [...el];
    }
    if (Array.isArray(el)) {
        var results = [];

        for (var node of el) {
            results.push(...node.querySelectorAll(sel));
        }
        return results;
    } else {
        return $(el).querySelectorAll(sel);
    }
}

var parser = document.createElement('div');
var relayingPeers = new Set(); // set of peerId
var interfaceIP = null;

const chatProtocol = '/x/chat';
const relayProtocol = '/x/chat-relay';
const callbackProtocol = '/x/chat-callback';

const chatCommands = Object.freeze({
    message: true,
    user: true,
    users: true,
    addUser: true,
    removeUser: true,
});

const chatState = Object.freeze({
    disconnected: 0,
    abortingRelayHosting: 1,
    abortingRelayConnection: 2,
    stoppingHosting: 3,
    disconnectingFromHost: 4,
    disconnectingFromRelayForHosting: 5,
    disconnectingFromRelayForConnection: 6,
    connectingToHost: 7,
    connectingToRelayForHosting: 8,
    connectingToRelayForConnection: 9,
    awaitingToken: 10,
    connectedToHost: 11,
    hostingDirectly: 12,
    connectedToRelayForHosting: 13,
    connectedToRelayForConnection: 14,
});

// TODO split this into server and client command handlers
class ChatHandler extends CommandHandler {
    constructor(connections) {
        super(null, connections, chatCommands, null, []);
        this.connections = connections || this;
        this.msgs = [];
        this.reset();
        this.hosting = new Map();
        this.userMap = new Map();             // peerID -> user
    }
    setProtocol(prot) {
        this.protocol = prot;
        this.protocols.add(prot);
    }
    // P2P API
    ident(status, peerID, addresses) {
        this.retrieveInfo();
        $('#natStatus').textContent = status;
        $('#peerID').textContent = peerID;
        console.log('IDENT: ', peerID, ' ', status);
        document.body.classList.add('hasNat');
        document.body.classList.add(status == natStatus.private ? 'privateNAT' : 'publicNAT');
        this.peerAddrs = addresses;
        this.reset();
        if (this.userName) {
            this.showGui();
        }
        this.storeInfo();
        super.ident(status, peerID, addresses);
    }
    // P2P API
    listening(protocol) {
        if (protocol == this.protocol) { // hosting directly
            this.sessionID = {
                type: 'peerAddr',
                peerID: this.connections.peerID,
                protocol: this.protocol,
                addrs: this.peerAddrs
            };

            $('#connectString').value = encodeObject(this.sessionID);
            this.connection.listening = true;
            delete this.connection.disconnected;
            this.changeState(chatState.hostingDirectly);
            this.showHideChats(true);
        }
        super.listening(protocol);
    }
    // P2P API
    listenerConnection(conID, peerID, prot) {
        if (prot == this.protocol) {
            var users = [];

            console.log("Got connection "+conID+" for protocol "+prot+" from peer "+peerID);
            this.hosting.set(conID, {conID: conID, peerID, protocol: prot});
            //this.connection = {connected: true, hosted: true};
        } else if (prot == callbackProtocol) { // got a callback, verify token later
            this.awaitingToken = true;
        }
        super.listenerConnection(conID, peerID, prot);
    }
    // P2P API
    peerConnection(conID, peerID, protocol) {
        super.peerConnection(conID, peerID, protocol);
        if (this.callbacks && this.callbacks.has(peerID)) { // patch connection to look like it's incoming
            var con = getConnectionInfo(conID);
            con.protocol = this.protocol;
            return;
        }
        switch (this.state) {
        case chatState.abortingRelayHosting:
        case chatState.abortingRelayConnection:
            this.changeState(chatState.disconnected);
            this.connection = {disconnected: true};
            close(conID);
            break;
        case chatState.connectingToHost: // connected directly to host
            if (peerID != this.chatHost) {
                alert('Connected to unexpected host: '+peerID);
            } else {
                this.changeState(chatState.connectedToHost);
                this.connectedToHost(conID, protocol, peerID);
            }
            break;
        case chatState.connectingToRelayForHosting: // connected to relay
            if (peerID != this.requestedRelayPeer) {
                alert('Connected to unexpected host: ' + peerID + ', expecting relay peer: ' + this.requestedRelayPeer);
            } else {
                sendObject(conID, {
                    name: 'requestHosting',
                    protocol: this.protocol,
                });
                this.sessionID = {
                    peerID: this.connections.peerID,
                    relayID: this.requestedRelayPeer,
                    protocol: this.protocol,
                    addrs: this.relayInfo.addrs,
                };

                this.relayConID = conID;
                //this.relayService = new RelayHost(this, {
                this.delegate = new RelayHost(this.connections, this, {
                    //receiveRelay: this.receiveRelay.bind(this),
                    receiveRelayConnectionFromPeer: this.receiveRelayConnectionFromPeer.bind(this),
                    //receiveRelayCallbackRequest: this.receiveRelayCallbackRequest.bind(this),
                    relayConnectionClosed: this.relayConnectionClosed.bind(this),
                }, relayProtocol);
                this.commandConnections.add(conID);
                //this.connections.infoByConID.get(conID).hosted = true;
                this.connection.hosted = true;
                this.hosting.set(conID, {conID: conID, peerID, protocol});
                $('#connectString').value = encodeObject(this.sessionID);
                this.changeState(chatState.connectedToRelayForHosting);
            }
            break;
        case chatState.connectingToRelayForConnection: // connected to relay
            if (peerID != this.requestedRelayPeer) {
                alert('Connected to unexpected host: ' + peerID + ', expecting relay peer: ' + this.requestedRelayPeer);
            } else {
                this.delegate = new RelayPeer(this.connections, this, {
                    //receiveRelay: this.receiveRelay.bind(this),
                    //receiveRelayCallbackRequest: this.receiveRelayCallbackRequest.bind(this),
                    receiveRelayConnectionToHost: this.receiveRelayConnectionToHost.bind(this),
                    relayConnectionClosed: this.relayConnectionClosed.bind(this),
                }, relayProtocol);
                sendObject(conID, {
                    name: 'requestRelaying',
                    peerID: this.chatHost,
                    protocol: this.chatProtocol,
                });
                this.relayConID = conID;
            }
            break;
        case chatState.hostingDirectly: // got new direct chat connection -- nothing more needed
        case chatState.disconnected: // these next cases should never happen
        case chatState.stoppingHosting:
        case chatState.disconnectingFromHost:
        case chatState.disconnectingFromRelayForHosting:
        case chatState.disconnectingFromRelayForConnection:
        case chatState.connectedToHost:
        case chatState.connectedToRelayForHosting:
        case chatState.awaitingToken:
            break;
        }
    }
    // P2P API
    data(conID, data, obj) {
        var con = getConnectionInfo(this.connections, conID);

        if (this.state == chatState.awaitingToken && con.protocol == callbackProtocol) {
            if (getString(data) == this.awaitingToken) {
                this.awaitingToken = null;
                this.changeState(chatState.connectedToHost);
                stop(callbackProtocol);
            } else {
                close(conID);
            }
        } else {
            super.data(conID, data, obj);
        }
    }
    // P2P API
    peerConnectionRefused(peerID, prot, msg) {
        this.reset()
        $('#toHostID').value = 'Failed to connect to '+peerID+' on protocol '+prot;
    }
    // P2P API
    connectionClosed(conID, msg) {
        if (this.state = chatState.connectedToRelayForHosting && conID == this.relayConnection) {
            this.relayConnection = null;
            this.reset();
        } else if (this.connection.hosted) {
            var con = this.hosting.get(conID);

            if (con) {
                this.hosting.delete(conID);
                this.userMap.delete(con.peerID);
                for (var [id, con] of this.hosting) {
                    if (id != conID) {
                        this.sendObject(id, {name: 'removeUser', peerID: con.peerID});
                    }
                }
                this.showUsers()
            }
            if (this.relayHost == conID) {
                this.relayHost = null;
            }
        } else {
            if (this.connection.conID == conID) {
                this.reset();
            }
        }
        super.connectionClosed(conID, msg);
    }
    // P2P API
    listenerClosed(protocol) {
        if (protocol == chatProtocol) {
            this.reset();
        }
        super.listenerClosed(protocol);
    }
    // RELAY API
    requestHosting(info, {protocol}) { // only allow hosting request from the host peer
        this.relayHost = info.conID;
        //this.showRelayInfo();
        this.showHideChats(true);
        this.commandConnections.add(info.conID);
        this.connection.connected = true;
        this.connection.conID = info.conID;
        $('#relayForhost').textContent = 'Stop relaying';
        this.sendObject(info.conID, {name: 'user', user: this.userName});
    }
    // RELAY API
    //requestCallback(info, {peerID, protocol, callbackProtocol, token}) {}
    // RELAY API
    //requestRelaying(info, {peerID, protocol}) {}
    // RELAY API
    //closeRelayConnection(info, {peerID, protocol}) {}
    // RELAY API
    //relay(conID, {peerID, protocol, command}) {} // no need to interfere here
    // RELAY API
    //receiveRelay(info, {peerID, protocol, command}) {}
    // RELAY API
    receiveRelayConnectionToHost(info, {peerID, protocol}) {
        info = getInfoForPeerAndProtocol(this.connections, peerID, protocol);
        if (info) {
            this.changeState(chatState.connectingToHost);
            this.peerConnection(info.conID, peerID, protocol);
        }
    }
    // RELAY API
    receiveRelayConnectionFromPeer(info, {peerID, protocol}) {
        info = getInfoForPeerAndProtocol(this.connections, peerID, protocol);
        if (info) {
            this.listenerConnection(info.conID, info.peerID, protocol);
        }
    }
    // RELAY API
    //receiveRelayCallbackRequest(info, {peerID, protocol, command}) {}
    // RELAY API
    //receiveRelay(info, {peerID, protocol, command}) {}
    // RELAY API
    relayConnectionClosed(conID) {
    }
    // chat API message
    message(info, msg) {
        this.addMessage(msg, false);
        for (var [conID, con] of this.hosting) {
            if (conID != info.conID) {
                this.sendObject(conID, msg);
            }
        }
    }
    // chat API user
    user(info, cmd) {
        if (this.connection.hosted) {
            var connection = this.hosting.get(info.conID);

            if (connection) {
                var users = {};

                connection.userName = cmd.user;
                this.userMap.set(connection.peerID, cmd.user);
                this.showUsers();
                for (var [peerID, user] of this.userMap) {
                    users[peerID] = user;
                }
                this.sendObject(info.conID, {name: 'users', users});
                for (var [conID, con] of this.hosting) {
                    if (conID != info.conID) {
                        this.sendObject(conID, {name: 'addUser', user: cmd.user, peerID: connection.peerID});
                    }
                }
            }
        }
    }
    // chat API users
    users(info, {users}) {
        this.userMap = new Map();
        for (var peerID in users) {
            this.userMap.set(peerID, users[peerID]);
        }
        this.showUsers();
    }
    // chat API addUser
    addUser(info, {peerID, user}) {
        this.userMap.set(peerID, user);
        this.showUsers();
    }
    // chat API removeUser
    removeUser(info, {peerID}) {
        this.userMap.delete(peerID);
        this.showUsers();
    }
    send(peerID, msg) { // send a message to a peer, optionally using relaying
        
    }
    addMessage(msg, me) {
        var html = "<div><span class='"+(me ? 'me' : msg.from)+"'>"+(me ? 'me' : msg.from)+": </span>";

        this.msgs.push(msg);
        $('#conversation').append(parse(html + msg.text));
    }
    showHideChats(show) {
        if (show) {
            document.body.classList.add('showChats');
            $('#showChats').textContent = 'Show Session Info';
        } else {
            document.body.classList.remove('showChats');
            $('#showChats').textContent = 'Show Chats';
        }
        if (this.connection.disconnected) {
            $('#chatsBody').classList.remove('connected');
        } else {
            $('#chatsBody').classList.add('connected');
        }
    }
    showUsers() {
        var users = [];

        for (var [peerID, user] of this.userMap) {
            users.push({peerID, user});
        }
        users.sort((a, b)=> a.user.localeCompare(b.user));
        $('#users').innerHTML = '';
        for (var user of users) {
            $('#users').innerHTML += "<div title='"+user.peerID+"'>"+user.user+"</div>";
        }
    }
    sendMessage(text) {
        var msg = {name: 'message', from: this.userName, text: text};

        if (this.hosting.size == 0 && !this.connection.connected) {
            text = "<i><b>[no connection]</b></i> "+text;
        }
        this.addMessage(msg, true);
        if (this.hosting.size > 0) {
            for (var [id, info] of this.hosting) {
                this.sendObject(id, msg);
            }
        } else if (this.connection.connected) {
            this.sendObject(this.connection.conID, msg);
        }
     }
    connectedToHost(conID, protocol, peerID) {
        if (peerID == this.requestedRelayPeer) {
            this.connection = {connected: true, conID: conID, peerID, protocol: protocol, relay: true};
            this.relayConnection = conID;
        } else {
            this.connection = {connected: true, conID: conID, peerID, protocol: protocol};
            $('#connectStatus').textContent = 'Connected to '+peerID+protocol;
            this.showHideChats(true);
            $('#connect').textContent = 'Disconnect';
            this.sendObject(conID, {name: 'user', user: this.userName});
        }
    }
    sendObject(conID, obj) {
        if (this.usingRelay) {
            this.relay.sendObject(conID, obj);
        } else if (conID < 0 && this.delegate instanceof RelayClient) {
            var info = this.connections.infoByConID.get(conID);

            sendObject(this.relayConID, {name: 'relay', peerID: info.peerID, protocol: info.protocol, command: obj});
        } else {
            sendObject(conID, obj);
        }
    }
    changeState(newState) {
        if (newState != chatState.disconnected) {
            document.body.classList.add('active');
        }
        if (!this.relayRequester) {
            switch(newState) {
            case chatState.hostingDirectly:
            case chatState.connectingToRelayForHosting:
                document.body.classList.add('host');
                break;
            case chatState.connectingToHost:
            case chatState.connectingToRelayForConnection:
            case chatState.awaitingToken:
                document.body.classList.add('peer');
                break;
            }
        }
        switch (newState) {
        case chatState.disconnected:
            document.body.classList.remove('active');
            document.body.classList.remove('peer');
            document.body.classList.remove('host');
            document.body.classList.remove('relay');
            $('#host').disabled = false;
            $('#host').textContent = 'Host';
            $('#hostingRelay').readOnly = false;
            $('#connect').disabled = false;
            $('#connect').textContent = 'Connect';
            break;
        case chatState.abortingRelayHosting:
            $('#host').textContent = 'Aborting Hosting...';
            $('#host').disabled = true;
            break;
        case chatState.abortingRelayConnection:
            $('#connect').textContent = 'Aborting Connection...';
            $('#connect').disabled = true;
            break;
        case chatState.stoppingHosting:
            $('#host').textContent = 'Stopping Hosting...';
            $('#host').disabled = true;
            break;
        case chatState.disconnectingFromHost:
            $('#connect').textContent = 'Disconnecting...';
            $('#connect').disabled = true;
            break;
        case chatState.connectingToRelayForHosting:
            $('#host').textContent = 'Abort Hosting';
            $('#connect').disabled = true;
            break;
        case chatState.connectingToHost:
        case chatState.connectingToRelayForConnection:
        case chatState.awaitingToken:
            $('#host').disabled = true;
            $('#hostingRelay').readOnly = true;
            $('#hostingRelay').value = '';
            $('#connect').textContent = 'Abort Connection';
            break;
        case chatState.connectedToHost:
            $('#connect').textContent = 'Disconnect';
            $('#connect').disabled = false;
            $('#toHostID').readOnly = true;
            break;
        case chatState.hostingDirectly:
        case chatState.connectedToRelayForHosting:
            $('#host').disabled = false;
            $('#host').textContent = 'Stop Hosting';
            $('#hostingRelay').readOnly = true;
            break;
        case chatState.disconnectingFromRelayForHosting:
            $('#host').disabled = true;
            $('#host').textContent = 'Stopping Hosting...';
            $('#hostingRelay').readOnly = true;
            break;
        case chatState.disconnectingFromRelayForConnection:
            $('#connect').textContent = 'Disconnecting...';
            $('#connect').disabled = true;
            $('#toHostID').readOnly = true;
            break;
        }
        this.state = newState;
    }
    reset() {
        this.state = chatState.disconnected;
        this.protocol = null;
        this.stopping = false;
        this.hostingDirectly = false;
        this.hostingThroughRelay = false;
        this.connection = {disconnected: true};
        this.showHideChats(false);
        $('#connectString').value = '';
        $('#connect').disabled = false;
        $('#connect').textContent = 'Connect';
        $('#toHostID').value = '';
        $('#toHostID').disabled = false;
        $('#send').value = '';
        this.userMap = new Map();
        if (this.userName && this.connections.peerID) {
            this.userMap.set(this.connections.peerID, this.userName);
            this.showUsers();
        }
        $('#relayForHost').disabled = this.connections.natStatus != natStatus.public && this.connections.natStatus != natStatus.maybePublic;
        $('#relayRequestHost').disabled = this.connections.natStatus != natStatus.public && this.connections.natStatus != natStatus.maybePublic;
    }
    setUser(name) {
        if (name != "" && name != this.userName) {
            this.userName = name;
            document.body.classList.add('hasUser');
            if (this.connections.natStatus != 'unknown') {
                this.showGui();
            }
            this.storeInfo();
        }
    }
    showGui() {
        document.body.classList.add('showGui');
        this.userMap.set(this.connections.peerID, this.userName);
        this.showUsers();
        this.showRelayInfo();
    }
    invalidConnection(msg) {
        $('#toHostID').value += ": " + msg;
        this.showHideChats(false);
    }
    handleCommand(info, data, obj) {
        if (!this.commands.has(obj.name) && this.isRelaying(info)) {
            return this.delegate.handleCommand(info, data, obj);
        } else {
            return super.handleCommand(info, data, obj);
        }
    }
    isRelaying(info) {
        return this.relayHost == info.conID || this.relayConID == info.conID || (this.relayService && this.relayService.isRelaying(info));
    }
    // A relay handles chat commands from its host
    shouldHandleCommand(info, data, obj) {
        return super.shouldHandleCommand(info, data, obj) || this.isRelaying(info);
    }
    relayFor(requestingPeer) {
        if (!this.relaying) {
            this.relayService = new RelayService(this.connections, null, {
                requestHosting: this.requestHosting.bind(this),
                //receiveRelayConnection: this.receiveRelayConnection.bind(this),
                //receiveRelayCallbackRequest: this.receiveRelayCallbackRequest.bind(this),
                //receiveRelay: this.receiveRelay.bind(this),
            }, relayProtocol, callbackProtocol);
            this.delegate = this.relayService;
            this.relaying = true;
            this.relayService.startRelay();
        }
        this.relayService.enableRelay(requestingPeer, chatProtocol);
        this.relayRequester = requestingPeer;
        document.body.classList.add('relay');
        $('#relayConnectString').value = encodeObject({
            type: 'relayAddr',
            relayID: this.connections.peerID,
            protocol: relayProtocol,
            addrs: this.peerAddrs
        });
        this.showRelayInfo();
    }
    useRelay(relayPeer) {
        var relayInfo = decodeObject(relayPeer);

        if (!relayInfo || relayInfo.type != 'relayAddr') {
            alert('bad relay string');
            return;
        }
        this.relayInfo = relayInfo;
        this.requestedRelayPeer = relayInfo.relayID;
        this.connection = {connecting: true, peerID: relayInfo.relayID, protocol: relayInfo.protocol, relay: true};
        this.changeState(chatState.connectingToRelayForHosting);
        this.callbacks = new Map();
        this.relayNextId = -1;
        this.relayConIDs = new Map();
        this.relayConnectionPeers = new Map();
        connect(encodePeerId(relayInfo.relayID, relayInfo.addrs), relayInfo.protocol, true);
    }
    showRelayInfo() {
        if (this.relaying && this.relayService.allowedHosts) {
            var cons = [...this.relayService.relayConnections.values()];
            var peersDiv = $('#relayingPeers');
            var conSet = new Set();

            peersDiv.innerHTML = '';
            cons.sort((a, b)=> a.rank != b.rank ? a.rank - b.rank : a.peerID.localCompare(b.peerID));
            for (var i of cons) {
                conSet.add(i.peerID);
                peersDiv.appendChild(relayDiv(i));
            }
            for (var [peerID, prots] of this.relayService.allowedHosts) {
                if (!conSet.has(peerID)) {
                    peersDiv.appendChild(pendingDiv(peerID, prots));
                }
            }
        }
    }
    storeInfo() {
        var info = {
            peerID: this.connections.peerID,
            userName: this.userName,
        };

        if (this.connection.relay) {
            info.relay = this.requestedRelayPeer;
        }
        if (this.relayConnection) {
            info.relayConnection = this.relayConnection;
        }
        if (this.connection.connected) {
            info.connection = this.connection;
        }
        localStorage.setItem('chat', JSON.stringify(info));
    }
    retrieveInfo() {
        var info = localStorage.getItem('chat');

        info = info && JSON.parse(info);
        if (info && info.peerID == this.connections.peerID) {
            this.userName = info.userName;
            this.connection = info.connection;
            if (info.relay) {
                this.requestedRelayPeer = info.relay;
            }
            if (info.relayConnection) {
                this.relayConnection = info.relayConnection;
            }
            $('#user').value = this.userName;
        }
    }
}

function encodeObject(obj) {
        return encode_ascii85(JSON.stringify(obj));
}

function decodeObject(str) {
    try {
        return JSON.parse(decode_ascii85(str));
    } catch (err) {
        return null;
    }
}

function relayDiv(info) {
    var html = info.peerID;

    if (info.isRelayHost) {
        html += ' [HOSTING]: ' + [...info.hostedProtocols].join(', ');
    }
    return parse("<div id='relay-"+info.peerID+"' title='"+html+"'>"+html+"</div>");
}

function pendingDiv(peerID, prots) {
    var html = "PENDING "+peerID+":";

    for (var prot of prots) {
        html += " " + prot;
    }
    return parse("<div id='relay-"+peerID+"' title='"+html+"'>"+html+"</div>");
}

function parse(html) {
    var result;

    parser.innerHTML = html;
    if (parser.firstChild == parser.lastChild) {
        result = parser.firstChild;
        parser.innerHTML = "";
    } else {
        result = parser;
        parser = document.createElement('div');
    }
    return result;
}

function encodePeerId(peerID, addrs) {
    return '/addrs/'+encode_ascii85(JSON.stringify({peerID, addrs}));
}

function start() {
    console.log("START");
    var url = "ws://"+document.location.host+"/";
    var connections = {};
    var handler = new ChatHandler(connections);
    var trackingHandler = new TrackingHandler(handler, connections);
    var search = document.location.search.match(/\?(.*)/);
    var params = null;

    if (search) {
        params = {};
        for (var param of search[1].split('&')) {
            var [k, v] = param.split('=');

            params[k] = v;
        }
        console.log('params:', JSON.stringify(params));
    }
    console.log('handler: ', handler);
    if (document.port) {
        url += ":" + document.port;
    }
    libp2p.start(url + "ipfswsrelay", new LoggingHandler(trackingHandler));
    $('#host').onclick = ()=> {
        switch (handler.state) {
        case chatState.disconnected: // start hosting
            if (handler.connections.natStatus == 'private' && !$('#hostingRelay').value) {
                alert('You must use a relay because your connection is private');
                return;
            }
            var protocol = chatProtocol + '-';
            var a = 'a'.charCodeAt(0);
            var A = 'A'.charCodeAt(0);

            for (var i = 0; i < 16; i++) {
                var n = Math.round(Math.random() * 51);

                protocol += String.fromCharCode(n < 26 ? a + n : A + n - 26);
            }
            handler.protocol = protocol;
            handler.protocols.add(protocol);
            if (handler.connections.natStatus == natStatus.public || handler.connections.natStatus == natStatus.maybePublic) {
                handler.hostingDirectly = true;
                $('#host').textContent = "Stop Hosting";
                $('#connect').disabled = true;
                $('#connectString').value = 'WAITING TO ESTABLISH LISTENER ON '+handler.protocol;
                listen(handler.protocol, true);
                handler.changeState(chatState.hostingDirectly);
            } else {
                handler.useRelay($('#hostingRelay').value);
            }
            break;
        case chatState.connectingToRelayForHosting: // abort connection
            handler.changeState(chatState.abortingRelayHosting);
            break;
        case chatState.hostingDirectly:  // stop hosting
            $('#host').disabled = true;
            stop(handler.protocol);
            handler.changeState(chatState.stoppingHosting);
            break;
        case chatState.connectedToRelayForHosting:  // stop hosting
            close(handler.relayConnection);
            handler.relayConnection = null;
            handler.changeState(chatState.stoppingHosting);
            break;
        case chatState.connectingToHost: // these should never happen; the host button is disabled
        case chatState.connectedToHost:
        case chatState.stoppingHosting:
        case chatState.connectingToRelayForConnection:
        case chatState.awaitingToken:
        case chatState.disconnectingFromHost:
        case chatState.disconnectingFromRelayForHosting:
        case chatState.disconnectingFromRelayForConnection:
        case chatState.abortingRelayHosting:
        case chatState.abortingRelayConnection:
            break;
        }
    };
    $('#connect').onclick = ()=> {
        switch (handler.state) {
        case chatState.disconnected: // connect
            var {peerID, relayID, protocol, addrs} = JSON.parse(decode_ascii85($('#toHostID').value));

            handler.chatHost = peerID;
            handler.chatProtocol = protocol;
            handler.protocols.add(protocol);
            if (relayID) {
                handler.requestedRelayPeer = relayID;
                if (handler.connections.natStatus != natStatus.public && handler.connections.natStatus != natStatus.maybePublic) {
                    handler.changeState(chatState.connectingToRelayForConnection);
                    connect(encodePeerId(relayID, addrs), relayProtocol, true);
                } else {
                    var token = '';

                    for (var i = 0; i < 16; i++) {
                        var n = Math.round(Math.random() * 51);

                        token += String.fromCharCode(n < 26 ? a + n : A + n - 26);
                    }
                    handler.token = token;
                    handler.changeState(chatState.awaitingToken);
                    connect(encodePeerId(relayID, addrs), callbackProtocol, true);
                }
            } else {
                handler.changeState(chatState.connectingToHost);
                connect(encodePeerId(peerID, addrs), protocol, true);
            }
            $('#connect').textContent = 'Abort Connection';
            handler.connection = {connecting: true, peerID, protocol: protocol};
            break;
        case chatState.connectingToHost:  // abort connection
        case chatState.connectingToRelayForConnection:
        case chatState.awaitingToken:
            handler.connection.abort = true;
            break;
        case chatState.connectedToHost: // disconnect
            close(handler.connection.conID);
            handler.changeState(chatState.disconnectingFromHost);
            break;
        case chatState.connectedToRelayForConnection: // disconnect
            close(handler.connection.conID);
            handler.changeState(chatState.disconnectingFromRelayForConnection);
            break;
        case chatState.abortingRelayHosting: // these should not happen; the connect button is disabled
        case chatState.abortingRelayConnection:
        case stoppingHosting:
        case disconnectingFromHost:
        case disconnectingFromRelayForHosting:
        case disconnectingFromRelayForConnection:
        case connectingToRelayForHosting:
        case hostingDirectly:
        case connectedToRelayForHosting:
            break;
        }
    };
    $('#send').onkeydown = evt=> {
        if (evt.key == 'Enter') {
            handler.sendMessage($('#send').value);
            $('#send').value = '';
        }
    };
    $('#user').onblur = ()=> handler.setUser($('#user').value);
    $('#user').onkeydown = evt=> {
        if (evt.key == 'Enter') {
            handler.setUser($('#user').value);
        }
    };
    $('#showChats').onclick = evt=> handler.showHideChats(!document.body.classList.contains('showChats'));
    $('#relayForHost').onclick = evt=> handler.relayFor($('#relayRequestHost').value);
}

window.onload = start;
