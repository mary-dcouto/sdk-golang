/*
	Copyright 2019 NetFoundry, Inc.

	Licensed under the Apache License, Version 2.0 (the "License");
	you may not use this file except in compliance with the License.
	You may obtain a copy of the License at

	https://www.apache.org/licenses/LICENSE-2.0

	Unless required by applicable law or agreed to in writing, software
	distributed under the License is distributed on an "AS IS" BASIS,
	WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
	See the License for the specific language governing permissions and
	limitations under the License.
*/

package edge

import (
	"github.com/michaelquigley/pfxlog"
	"github.com/openziti/foundation/channel2"
	"github.com/openziti/foundation/util/concurrenz"
	"sync"
	"sync/atomic"
)

type MsgSink interface {
	HandleMuxClose() error
	Id() uint32
	Accept(msg *channel2.Message)
}

type MsgMux interface {
	channel2.ReceiveHandler
	channel2.CloseHandler
	AddMsgSink(sink MsgSink) error
	RemoveMsgSink(sink MsgSink)
	RemoveMsgSinkById(sinkId uint32)
	Close()
}

func NewCowMapMsgMux() MsgMux {
	result := &CowMapMsgMux{}
	result.sinks.Store(map[uint32]MsgSink{})
	return result
}

type CowMapMsgMux struct {
	sync.Mutex
	closed  concurrenz.AtomicBoolean
	running concurrenz.AtomicBoolean
	sinks   atomic.Value
}

func (mux *CowMapMsgMux) ContentType() int32 {
	return ContentTypeData
}

func (mux *CowMapMsgMux) HandleReceive(msg *channel2.Message, ch channel2.Channel) {
	connId, found := msg.GetUint32Header(ConnIdHeader)
	if !found {
		pfxlog.Logger().Errorf("received edge message with no connId header. content type: %v", msg.ContentType)
		return
	}

	sinks := mux.getSinks()
	if sink, found := sinks[connId]; found {
		sink.Accept(msg)
	} else {
		pfxlog.Logger().Debugf("unable to dispatch msg received for unknown edge conn id: %v", connId)
	}
}

func (mux *CowMapMsgMux) HandleClose(channel2.Channel) {
	mux.Close()
}

func (mux *CowMapMsgMux) AddMsgSink(sink MsgSink) error {
	mux.updateSinkMap(func(m map[uint32]MsgSink) {
		m[sink.Id()] = sink
	})
	return nil
}

func (mux *CowMapMsgMux) RemoveMsgSink(sink MsgSink) {
	mux.RemoveMsgSinkById(sink.Id())
}

func (mux *CowMapMsgMux) RemoveMsgSinkById(sinkId uint32) {
	mux.updateSinkMap(func(m map[uint32]MsgSink) {
		delete(m, sinkId)
	})
}

func (mux *CowMapMsgMux) updateSinkMap(f func(map[uint32]MsgSink)) {
	mux.Lock()
	defer mux.Unlock()

	current := mux.getSinks()
	result := map[uint32]MsgSink{}
	for k, v := range current {
		result[k] = v
	}
	f(result)
	mux.sinks.Store(result)
}

func (mux *CowMapMsgMux) Close() {
	if mux.closed.CompareAndSwap(false, true) {
		// we don't need to lock the mux because due to the atomic bool, only one go-routine will enter this.
		// If the sink HandleMuxClose methods do anything with the mux, like remove themselves, they will acquire
		// their own locks
		sinks := mux.getSinks()
		for _, val := range sinks {
			if err := val.HandleMuxClose(); err != nil {
				pfxlog.Logger().
					WithField("sinkId", val.Id()).
					WithError(err).
					Error("error while closing message sink")
			}
		}
	}
}

func (mux *CowMapMsgMux) getSinks() map[uint32]MsgSink {
	return mux.sinks.Load().(map[uint32]MsgSink)
}
