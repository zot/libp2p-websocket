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

//TODO handle reconnecting to relay gracefully

import libp2p from "./protocol.js"

var $ = document.querySelector.bind(document);
var $all = document.querySelectorAll.bind(document);
var $find = (el, sel)=> el.querySelector(sel);
var $findAll = (el, sel)=> el.querySelectorAll(sel);
var parser = document.createElement('div');

class Commands {
    message(conid, cmd, delegate) {
        delegate.receiveMessage(conid, cmd);
    }
    user(conid, cmd, delegate) {
        delegate.receiveUser(conid, cmd)
    }
    users(conid, cmd, delegate) {
        delegate.newUsers(cmd);
    }
    addUser(conid, cmd, delegate) {
        delegate.addUser(cmd);
    }
    removeUser(conid, cmd, delegate) {
        delegate.removeUser(cmd);
    }
}

class ChatHandler extends libp2p.BlankHandler {
    constructor() {
        super();
        this.msgs = [];
        this.commands = new Commands(this);
        this.reset();
        this.hosting = new Map();
        this.userMap = new Map(); // peerid -> user
    }
    // P2P API
    ident(publicPeer, peerId) {
        $('#natStatus').textContent = this.natStatus;
        $('#peerID').textContent = peerId;
        console.log('IDENT: ', peerId, ' ', this.natStatus);
        document.body.classList.add('hasNat');
        if (this.user) {
            this.showGui();
        }
    }
    // P2P API
    listening(protocol) {
        $('#listenerStatus').value = this.peerId+this.protocol;
    }
    // P2P API
    listenerConnection(conid, peerid, prot) {
        this.gotConnection(conid, peerid, prot);
    }
    // P2P API
    discoveryHostConnect(conid, peerid, prot) {
        this.gotConnection(conid, peerid, prot);
    }
    // P2P API
    discoveryAwaitingCallback(protocol) {
        if (this.connection.connecting) {
            $('#connectStatus').textContent = 'Awaiting callback for '+protocol;
            $('#connectDiv').classList.add('connected');
        }
    }
    // P2P API
    peerConnection(conid, peerid, protocol) {
        if (this.connection.abort) {
            libp2p.close(conid);
            this.connection = {disconnected: true};
        } else if (this.connection.connecting) {
            this.connectedToHost(conid, protocol, peerid);
        }
    }
    // P2P API
    discoveryPeerConnect(conid, protocol, peerid) {
        if (this.connection.connecting) {
            this.connectedToHost(conid, protocol, peerid);
        }
    }
    // P2P API
    connectionClosed(conid, msg) {
        if (this.connection.hosted) {
            var con = this.hosting.get(conid);

            if (con) {
                this.hosting.delete(conid);
                this.userMap.delete(con.peer);
                for (var [id, con] of this.hosting) {
                    if (id != conid) {
                        libp2p.sendObject(id, {name: 'removeUser', peer: con.peer});
                    }
                }
                this.showUsers()
            }
        } else {
            if (this.connection.connectionId == conid) {
                this.reset();
            }
        }
    }
    // P2P API
    data(conid, data) {
        var cmd = JSON.parse(libp2p.getString(data));

        console.log("RECEIVED COMMAND: "+JSON.stringify(cmd), cmd);
        this.commands[cmd.name](conid, cmd, this);
    }
    // P2P API
    listenerClosed(protocol) {
        if (protocol == this.protocol) {
            this.reset();
        }
    }
    // message command
    receiveMessage(conid, msg) {
        this.addMessage(msg, false);
        for (var [id, con] of this.hosting) {
            if (id != conid) {
                libp2p.sendObject(id, msg);
            }
        }
    }
    // user command
    receiveUser(conid, cmd) {
        if (this.connection.hosted) {
            var connection = this.hosting.get(conid);

            if (connection) {
                var users = {};

                connection.user = cmd.user;
                this.userMap.set(connection.peer, cmd.user);
                this.showUsers();
                for (var [peer, user] of this.userMap) {
                    users[peer] = user;
                }
                libp2p.sendObject(conid, {name: 'users', users: users});
                for (var [id, con] of this.hosting) {
                    if (id != conid) {
                        libp2p.sendObject(id, {name: 'addUser', user: cmd.user, peer: connection.peer});
                    }
                }
            }
        }
    }
    // users command
    newUsers(cmd) {
        this.userMap = new Map();
        for (var peer in cmd.users) {
            this.userMap.set(peer, cmd.users[peer]);
        }
        this.showUsers();
    }
    // addUser command
    addUser(cmd) {
        this.userMap.set(cmd.peer, cmd.user);
        this.showUsers();
    }
    // removeUser command
    removeUser(cmd) {
        this.userMap.delete(cmd.peer);
        this.showUsers();
    }
    addMessage(msg, me) {
        var html = "<div><span class='"+(me ? 'me' : msg.from)+"'>"+(me ? 'me' : msg.from)+": </span>";

        this.msgs.push(msg);
        $('#conversation').append(parse(html + msg.text));
    }
    gotConnection(conid, peerid, prot) {
        var users = [];

        console.log("Got connection "+conid+" for protocol "+prot+" from peer "+peerid);
        this.hosting.set(conid, {connectionId: conid, peer: peerid, protocol: prot});
        this.connection = {connected: true, hosted: true};
    }
    showUsers() {
        var users = [];

        for (var [peer, user] of this.userMap) {
            users.push({peer: peer, user: user});
        }
        users.sort((a, b)=> a.user.localeCompare(b.user));
        $('#users').innerHTML = '';
        for (var user of users) {
            $('#users').innerHTML += "<div title='"+user.peer+"'>"+user.user+"</div>";
        }
    }
    sendMessage(text) {
        var msg = {name: 'message', from: this.user, text: text};

        if (this.hosting.size == 0 && !this.connection.connected) {
            text = "<i><b>[no connection]</b></i> "+text;
        }
        this.addMessage(msg, true);
        if (this.hosting.size > 0) {
            for (var [id, info] of this.hosting) {
                libp2p.sendObject(id, msg);
            }
        } else if (this.connection.connected) {
            libp2p.sendObject(this.connection.connectionId, msg);
        }
     }
    connectedToHost(conid, protocol, peerid) {
        this.connection = {connected: true, connectionId: conid, peer: peerid, protocol: protocol};
        $('#connectStatus').textContent = 'Connected to '+peerid+protocol;
        $('#connectDiv').classList.add('connected');
        $('#connect').textContent = 'Disconnect';
        libp2p.sendObject(conid, {name: 'user', user: this.user});
    }
    reset() {
        this.protocol = null;
        this.stopping = false;
        this.connection = {disconnected: true};
        $('#connectDiv').classList.remove('connected');
        $('#host').textContent = 'Host';
        $('#host').disabled = false;
        $('#connect').disabled = false;
        $('#connect').textContent = 'Connect';
        $('#toHostID').value = '';
        $('#listenerStatus').value = '';
        $('#send').value = '';
        this.userMap = new Map();
        if (this.user && this.peerId) {
            this.userMap.set(this.peerId, this.user);
            this.showUsers();
        }
    }
    setUser(name) {
        if (name != "" && name != this.user) {
            this.user = name;
            document.body.classList.add('hasUser');
            if (this.natStatus != 'unknown') {
                this.showGui();
            }
        }
    }
    showGui() {
        document.body.classList.add('showGui');
        this.userMap.set(this.peerId, this.user);
        this.showUsers();
    }
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

function start() {
    console.log("START");
    var url = "ws://"+document.location.host+"/";
    var handler = new ChatHandler();

    console.log('handler: ', handler);
    if (document.port) {
        url += ":" + document.port;
    }
    libp2p.connect(url + "ipfswsrelay", new libp2p.LoggingHandler(new libp2p.TrackingHandler(handler)));
    $('#host').onclick = ()=> {
        if (!handler.stopping) {
            if (handler.protocol) {
                handler.stopping = true;
                libp2p.stop(handler.protocol);
            } else {
                var protocol = "/x/chat-";
                var a = 'a'.charCodeAt(0);
                var A = 'A'.charCodeAt(0);

                for (var i = 0; i < 16; i++) {
                    var n = Math.round(Math.random() * 51);

                    protocol += String.fromCharCode(n < 26 ? a + n : A + n - 26);
                }
                handler.protocol = protocol;
                $('#host').textContent = "Stop Listening";
                $('#connect').disabled = true;
                $('#listenerStatus').value = 'WAITING TO ESTABLISH LISTENER ON '+protocol;
                libp2p.discoveryListen(protocol, true);
            }
        }
    };
    $('#connect').onclick = ()=> {
        if (handler.connection.disconnected) { // connect
            var peerIdProt = $('#toHostID').value;
            var slashIndex = peerIdProt.indexOf('/');

            if (slashIndex == -1) {
                invalidConnection('BAD CONNECT FORMAT: '+peerIdProt);
            } else {
                var peerId = peerIdProt.slice(0, slashIndex);
                var prot = peerIdProt.slice(slashIndex);
                var invalidProt = libp2p.checkProt(prot);

                if (invalidProt) {
                    invalidConnection(invalidProt);
                } else {
                    $('#host').disabled = true;
                    $('#connectStatus').value = 'WAITING TO CONNECT TO: '+peerIdProt;
                    libp2p.discoveryConnect(peerId, prot, true);
                    $('#connect').textContent = 'Abort Connection';
                    handler.connection = {connecting: true, peer: peerId, protocol: prot};
                }
            }
        } else if (handler.connection.connecting) { // abort
            handler.connection.abort = true;
        } else { // disconnect
            libp2p.close(handler.connection.connectionId);
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
}

function invalidConnection(msg) {
    $('#toHostID').value += ": " + msg;
    $('#connectDiv').classList.remove('connected');
}

window.onload = start;
