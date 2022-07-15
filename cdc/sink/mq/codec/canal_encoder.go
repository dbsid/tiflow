// Copyright 2022 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package codec

import (
	"context"

	"github.com/golang/protobuf/proto"
	"github.com/pingcap/errors"
	"github.com/pingcap/log"
	"github.com/pingcap/tiflow/cdc/model"
	"github.com/pingcap/tiflow/pkg/config"
	cerror "github.com/pingcap/tiflow/pkg/errors"
	canal "github.com/pingcap/tiflow/proto/canal"
	"go.uber.org/zap"
)

// canalBatchEncoder encodes the events into the byte of a batch into.
type canalBatchEncoder struct {
	messages     *canal.Messages
	callbackBuf  []func()
	packet       *canal.Packet
	entryBuilder *canalEntryBuilder
}

// EncodeCheckpointEvent implements the EventBatchEncoder interface
func (d *canalBatchEncoder) EncodeCheckpointEvent(ts uint64) (*MQMessage, error) {
	// For canal now, there is no such a corresponding type to ResolvedEvent so far.
	// Therefore, the event is ignored.
	return nil, nil
}

// AppendRowChangedEvent implements the EventBatchEncoder interface
func (d *canalBatchEncoder) AppendRowChangedEvent(
	_ context.Context,
	_ string,
	e *model.RowChangedEvent,
	callback func(),
) error {
	entry, err := d.entryBuilder.fromRowEvent(e)
	if err != nil {
		return errors.Trace(err)
	}
	b, err := proto.Marshal(entry)
	if err != nil {
		return cerror.WrapError(cerror.ErrCanalEncodeFailed, err)
	}
	d.messages.Messages = append(d.messages.Messages, b)
	if callback != nil {
		d.callbackBuf = append(d.callbackBuf, callback)
	}
	return nil
}

// EncodeDDLEvent implements the EventBatchEncoder interface
func (d *canalBatchEncoder) EncodeDDLEvent(e *model.DDLEvent) (*MQMessage, error) {
	entry, err := d.entryBuilder.fromDDLEvent(e)
	if err != nil {
		return nil, errors.Trace(err)
	}
	b, err := proto.Marshal(entry)
	if err != nil {
		return nil, cerror.WrapError(cerror.ErrCanalEncodeFailed, err)
	}

	messages := new(canal.Messages)
	messages.Messages = append(messages.Messages, b)
	b, err = messages.Marshal()
	if err != nil {
		return nil, cerror.WrapError(cerror.ErrCanalEncodeFailed, err)
	}

	packet := &canal.Packet{
		VersionPresent: &canal.Packet_Version{
			Version: CanalPacketVersion,
		},
		Type: canal.PacketType_MESSAGES,
	}
	packet.Body = b
	b, err = packet.Marshal()
	if err != nil {
		return nil, cerror.WrapError(cerror.ErrCanalEncodeFailed, err)
	}

	return newDDLMsg(config.ProtocolCanal, nil, b, e), nil
}

// Build implements the EventBatchEncoder interface
func (d *canalBatchEncoder) Build() []*MQMessage {
	rowCount := len(d.messages.Messages)
	if rowCount == 0 {
		return nil
	}

	err := d.refreshPacketBody()
	if err != nil {
		log.Panic("Error when generating Canal packet", zap.Error(err))
	}

	value, err := proto.Marshal(d.packet)
	if err != nil {
		log.Panic("Error when serializing Canal packet", zap.Error(err))
	}
	ret := newMsg(config.ProtocolCanal, nil, value, 0, model.MessageTypeRow, nil, nil)
	ret.SetRowsCount(rowCount)
	d.messages.Reset()
	d.resetPacket()

	if len(d.callbackBuf) != 0 && len(d.callbackBuf) == rowCount {
		callbacks := d.callbackBuf
		ret.Callback = func() {
			for _, cb := range callbacks {
				cb()
			}
		}
		d.callbackBuf = make([]func(), 0)
	}
	return []*MQMessage{ret}
}

// refreshPacketBody() marshals the messages to the packet body
func (d *canalBatchEncoder) refreshPacketBody() error {
	oldSize := len(d.packet.Body)
	newSize := proto.Size(d.messages)
	if newSize > oldSize {
		// resize packet body slice
		d.packet.Body = append(d.packet.Body, make([]byte, newSize-oldSize)...)
	} else {
		d.packet.Body = d.packet.Body[:newSize]
	}

	_, err := d.messages.MarshalToSizedBuffer(d.packet.Body)
	return err
}

func (d *canalBatchEncoder) resetPacket() {
	d.packet = &canal.Packet{
		VersionPresent: &canal.Packet_Version{
			Version: CanalPacketVersion,
		},
		Type: canal.PacketType_MESSAGES,
	}
}

// newCanalBatchEncoder creates a new canalBatchEncoder.
func newCanalBatchEncoder() EventBatchEncoder {
	encoder := &canalBatchEncoder{
		messages:     &canal.Messages{},
		callbackBuf:  make([]func(), 0),
		entryBuilder: newCanalEntryBuilder(),
	}

	encoder.resetPacket()
	return encoder
}

type canalBatchEncoderBuilder struct{}

// Build a `canalBatchEncoder`
func (b *canalBatchEncoderBuilder) Build() EventBatchEncoder {
	return newCanalBatchEncoder()
}

func newCanalBatchEncoderBuilder() EncoderBuilder {
	return &canalBatchEncoderBuilder{}
}