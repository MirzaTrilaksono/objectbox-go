package iot

import (
	. "github.com/objectbox/objectbox-go/objectbox"
	"github.com/objectbox/objectbox-go/test/assert"
	"github.com/objectbox/objectbox-go/test/model/iot/binding"
	"github.com/objectbox/objectbox-go/test/model/iot/object"
	"strconv"
)

func CreateObjectBox() *ObjectBox {
	builder := NewObjectBoxBuilder().Name("iot-test").LastEntityId(2, 10002)
	//objectBox.SetDebugFlags(DebugFlags_LOG_ASYNC_QUEUE)
	builder.RegisterBinding(binding.EventBinding{})
	builder.RegisterBinding(binding.ReadingBinding{})
	objectBox, err := builder.Build()
	if err != nil {
		panic(err)
	}
	return objectBox
}

func PutEvent(ob *ObjectBox, device string, date int64) uint64 {
	event := object.Event{Device: device, Date: date}
	id, err := ob.Box(1).Put(&event)
	assert.NoErr(nil, err)
	return id
}

func PutEvents(ob *ObjectBox, count int) {
	// TODO TX
	for i := 1; i <= count; i++ {
		PutEvent(ob, "device "+strconv.Itoa(i), int64(10000+i))
	}
}
