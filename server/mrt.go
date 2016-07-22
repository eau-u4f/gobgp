// Copyright (C) 2016 Nippon Telegraph and Telephone Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"bytes"
	"os"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/osrg/gobgp/packet/mrt"
)

type mrtWriter struct {
	dead     chan struct{}
	s        *BgpServer
	filename string
	file     *os.File
	interval uint64
}

func (m *mrtWriter) Stop() {
	close(m.dead)
}

func (m *mrtWriter) loop() error {
	w := m.s.Watch(WatchUpdate(false))
	c := func() *time.Ticker {
		if m.interval == 0 {
			return &time.Ticker{}
		}
		return time.NewTicker(time.Second * time.Duration(m.interval))
	}()

	defer func() {
		if m.file != nil {
			m.file.Close()
		}
		if m.interval != 0 {
			c.Stop()
		}
		w.Stop()
	}()

	for {
		serialize := func(ev watcherEvent) ([]byte, error) {
			m := ev.(*watcherEventUpdateMsg)
			subtype := mrt.MESSAGE_AS4
			mp := mrt.NewBGP4MPMessage(m.peerAS, m.localAS, 0, m.peerAddress.String(), m.localAddress.String(), m.fourBytesAs, nil)
			mp.BGPMessagePayload = m.payload
			if m.fourBytesAs == false {
				subtype = mrt.MESSAGE
			}
			bm, err := mrt.NewMRTMessage(uint32(m.timestamp.Unix()), mrt.BGP4MP, subtype, mp)
			if err != nil {
				log.WithFields(log.Fields{
					"Topic": "mrt",
					"Data":  m,
				}).Warn(err)
				return nil, err
			}
			return bm.Serialize()
		}

		drain := func(ev watcherEvent) {
			events := make([]watcherEvent, 0, 1+len(w.Event()))
			if ev != nil {
				events = append(events, ev)
			}

			for len(w.Event()) > 0 {
				events = append(events, <-w.Event())
			}

			w := func(buf []byte) {
				if _, err := m.file.Write(buf); err == nil {
					m.file.Sync()
				} else {
					log.WithFields(log.Fields{
						"Topic": "mrt",
						"Error": err,
					}).Warn(err)
				}
			}

			var b bytes.Buffer
			for _, e := range events {
				buf, err := serialize(e)
				if err != nil {
					log.WithFields(log.Fields{
						"Topic": "mrt",
						"Data":  e,
					}).Warn(err)
					continue
				}
				b.Write(buf)
				if b.Len() > 1*1000*1000 {
					w(b.Bytes())
					b.Reset()
				}
			}
			if b.Len() > 0 {
				w(b.Bytes())
			}
		}
		select {
		case <-m.dead:
			drain(nil)
			return nil
		case e := <-w.Event():
			drain(e)
		case <-c.C:
			m.file.Close()
			file, err := mrtFileOpen(m.filename, m.interval)
			if err == nil {
				m.file = file
			} else {
				log.Info("can't rotate mrt file", err)
			}
		}
	}
}

func mrtFileOpen(filename string, interval uint64) (*os.File, error) {
	realname := filename
	if interval != 0 {
		realname = time.Now().Format(filename)
	}

	i := len(realname)
	for i > 0 && os.IsPathSeparator(realname[i-1]) {
		// skip trailing path separators
		i--
	}
	j := i

	for j > 0 && !os.IsPathSeparator(realname[j-1]) {
		j--
	}

	if j > 0 {
		if err := os.MkdirAll(realname[0:j-1], 0755); err != nil {
			log.Warn(err)
			return nil, err
		}
	}

	file, err := os.OpenFile(realname, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		log.Warn(err)
	}
	return file, err
}

func newMrtWriter(s *BgpServer, dumpType int, filename string, interval uint64) (*mrtWriter, error) {
	file, err := mrtFileOpen(filename, interval)
	if err != nil {
		return nil, err
	}
	m := mrtWriter{
		s:        s,
		filename: filename,
		file:     file,
		interval: interval,
	}
	go m.loop()
	return &m, nil
}
