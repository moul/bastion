package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	gssh "github.com/gliderlabs/ssh"
)

func main() {
	sshBastionTarget, sshBastionPassword := os.Getenv("SSH_BASTION_TARGET"), os.Getenv("SSH_BASTION_PASSWORD")
	if sshBastionTarget == "" || sshBastionPassword == "" {
		fmt.Printf("Please set SSH_BASTION_TARGET and SSH_BASTION_PASSWORD\n")
		fmt.Printf("EXAMPLE:\n")
		fmt.Printf("export SSH_BASTION_PASSWORD=password\n")
		fmt.Printf("export SSH_BASTION_TARGET=192.168.1.62:22\n")
		os.Exit(1)
	}

	gssh.Handle(func(s gssh.Session) {
		// authorizedKey := gossh.MarshalAuthorizedKey(s.PublicKey())
		// io.WriteString(s, fmt.Sprintf("public key used by %s:\n", s.User()))
		// s.Write(authorizedKey)

		clientConfig := &ssh.ClientConfig{
			User:            "arkan",
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Auth: []ssh.AuthMethod{
				ssh.Password(sshBastionPassword),
			},
		}

		remoteConn, err := ssh.Dial("tcp", sshBastionTarget, clientConfig)
		if err != nil {
			log.Println("[ERROR] SSH connection failed", err)
			return
		}
		defer remoteConn.Close()

		channel2, reqs2, err := remoteConn.OpenChannel("session", []byte{})
		if err != nil {
			log.Println("[ERROR] SSH connection open channel failed", err)
			return
		}

		log.Println("[INFO] SSH connecion established")
		defer log.Println("[INFO] SSH connecion closed")

		proxy(s.MaskedReqs(), reqs2, s, channel2)

	})

	publicKeyOption := gssh.PublicKeyAuth(func(ctx gssh.Context, key gssh.PublicKey) bool {
		return true // allow all keys, or use ssh.KeysEqual() to compare against known keys
	})

	log.Println("starting ssh server on port 2222...")
	log.Fatal(gssh.ListenAndServe(":2222", nil, publicKeyOption))
}

func proxy(reqs1, reqs2 <-chan *ssh.Request, channel1 ssh.Channel, channel2 ssh.Channel) {
	wrappedChannel1 := newLoggedChannel(channel1)
	var closer sync.Once
	closeFunc := func() {
		wrappedChannel1.Close()
		channel2.Close()
	}

	defer closer.Do(closeFunc)

	closerChan := make(chan bool, 1)

	// From remote, to client.
	go func() {
		_, _ = io.Copy(wrappedChannel1, channel2)
		closerChan <- true
	}()

	go func() {
		_, _ = io.Copy(channel2, channel1)
		closerChan <- true
	}()

	for {
		select {
		case req := <-reqs1:
			if req == nil {
				return
			}
			b, err := channel2.SendRequest(req.Type, req.WantReply, req.Payload)
			if err != nil {
				return
			}
			req.Reply(b, nil)
		case req := <-reqs2:
			if req == nil {
				return
			}
			b, err := channel1.SendRequest(req.Type, req.WantReply, req.Payload)
			if err != nil {
				return
			}
			req.Reply(b, nil)
		case <-closerChan:
			return
		}
	}
}

type logChannel struct {
	channel ssh.Channel
	file    *os.File
}

func writeTTYRecHeader(fd io.Writer, length int) {
	t := time.Now()

	tv := syscall.NsecToTimeval(t.UnixNano())

	binary.Write(fd, binary.LittleEndian, int32(tv.Sec))
	binary.Write(fd, binary.LittleEndian, int32(tv.Usec))
	binary.Write(fd, binary.LittleEndian, int32(length))
}

func newLoggedChannel(channel ssh.Channel) *logChannel {
	f, err := os.OpenFile("session.ttyrec", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0640)
	if err != nil {
		panic(err)
	}

	return &logChannel{
		channel: channel,
		file:    f,
	}
}

func (l *logChannel) Read(data []byte) (int, error) {
	return l.Read(data)
}

func (l *logChannel) Write(data []byte) (int, error) {
	writeTTYRecHeader(l.file, len(data))
	l.file.Write(data)

	return l.channel.Write(data)
}

func (l *logChannel) Close() error {
	l.file.Close()

	return l.channel.Close()
}
