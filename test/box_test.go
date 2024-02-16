/*
 * Copyright 2018-2021 ObjectBox Ltd. All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package objectbox_test

import (
	"errors"
	"github.com/objectbox/objectbox-go/test/assert"
	"github.com/objectbox/objectbox-go/test/model"
	"github.com/objectbox/objectbox-go/test/model/iot"
	"os"
	"testing"
)

func TestBox(t *testing.T) {
	env := iot.NewTestEnv()
	defer env.Close()
	box1 := iot.BoxForEvent(env.ObjectBox)
	box2 := iot.BoxForEvent(env.ObjectBox)

	assert.Eq(t, box1.Box, box2.Box)
}

func TestPutAsync(t *testing.T) {
	env := iot.NewTestEnv()
	defer env.Close()
	box := iot.BoxForEvent(env.ObjectBox)
	err := box.RemoveAll()
	assert.NoErr(t, err)

	event := iot.Event{
		Device: "my device",
	}
	objectId, err := box.PutAsync(&event)
	assert.NoErr(t, err)
	assert.Eq(t, objectId, event.Id)

	assert.NoErr(t, env.AwaitAsyncCompletion())

	count, err := box.Count()
	assert.NoErr(t, err)
	assert.Eq(t, uint64(1), count)

	eventRead, err := box.Get(objectId)
	assert.NoErr(t, err)
	if objectId != eventRead.Id || event.Device != eventRead.Device {
		t.Fatalf("Event data error: %v vs. %v", event, eventRead)
	}

	err = box.Remove(eventRead)
	assert.NoErr(t, err)

	eventRead, err = box.Get(objectId)
	assert.NoErr(t, err)
	if eventRead != nil {
		t.Fatalf("object hasn't been deleted by box.Remove()")
	}

	count, err = box.Count()
	assert.NoErr(t, err)
	assert.Eq(t, uint64(0), count)
}

func TestUnique(t *testing.T) {
	env := iot.NewTestEnv()
	defer env.Close()
	box := iot.BoxForEvent(env.ObjectBox)

	err := box.RemoveAll()
	assert.NoErr(t, err)

	_, err = box.Put(&iot.Event{
		Device: "my device",
		Uid:    "duplicate-uid",
	})
	assert.NoErr(t, err)

	_, err = box.Put(&iot.Event{
		Device: "my device 2",
		Uid:    "duplicate-uid",
	})
	if err == nil {
		assert.Failf(t, "put() passed instead of an expected unique constraint violation")
	}

	count, err := box.Count()
	assert.NoErr(t, err)
	assert.Eq(t, uint64(1), count)
}

func TestBoxBulk(t *testing.T) {
	env := iot.NewTestEnv()
	defer env.Close()
	box := iot.BoxForEvent(env.ObjectBox)

	err := box.RemoveAll()
	assert.NoErr(t, err)

	event1 := iot.Event{
		Device: "Pi 3B",
	}
	event2 := iot.Event{
		Device: "Pi Zero",
	}
	events := []*iot.Event{&event1, &event2}
	objectIds, err := box.PutMany(events)
	assert.NoErr(t, err)
	assert.Eq(t, uint64(1), objectIds[0])
	assert.Eq(t, objectIds[0], events[0].Id)
	assert.Eq(t, uint64(2), objectIds[1])
	assert.Eq(t, objectIds[1], events[1].Id)

	count, err := box.Count()
	assert.NoErr(t, err)
	assert.Eq(t, uint64(2), count)

	eventRead, err := box.Get(objectIds[0])
	assert.NoErr(t, err)
	assert.Eq(t, "Pi 3B", eventRead.Device)

	eventRead, err = box.Get(objectIds[1])
	assert.NoErr(t, err)
	assert.Eq(t, "Pi Zero", eventRead.Device)

	// And passing nil & empty slice
	objectIds, err = box.PutMany(nil)
	assert.NoErr(t, err)
	assert.Eq(t, len(objectIds), 0)
	//noinspection GoPreferNilSlice
	noEvents := []*iot.Event{}
	objectIds, err = box.PutMany(noEvents)
	assert.NoErr(t, err)
	assert.Eq(t, len(objectIds), 0)

	contains, err := box.ContainsIds(events[0].Id, events[1].Id)
	assert.NoErr(t, err)
	assert.True(t, contains)

	contains, err = box.ContainsIds(100, events[0].Id, events[1].Id)
	assert.NoErr(t, err)
	assert.True(t, !contains)

	countRemoved, err := box.RemoveIds(100, events[0].Id)
	assert.NoErr(t, err)
	assert.Eq(t, uint64(1), countRemoved)

	countRemoved, err = box.RemoveMany(events...)
	assert.NoErr(t, err)
	assert.Eq(t, uint64(1), countRemoved)

	count, err = box.Count()
	assert.NoErr(t, err)
	assert.Eq(t, uint64(0), count)

}

func TestPut(t *testing.T) {
	env := iot.NewTestEnv()
	RunTestPut(t, env)
}

func TestPutInMemoryDB(t *testing.T) {
	var dir = "memory:iot-test"
	env := iot.NewTestEnvWithDir(t, dir)
	_, err := os.Stat(dir)
	assert.True(t, errors.Is(err, os.ErrNotExist)) // Must not exist in file system
	RunTestPut(t, env)
}

// Not sure if this is the best way to "parameterize" test...
func RunTestPut(t *testing.T, env *iot.TestEnv) {
	defer env.Close()
	box := iot.BoxForEvent(env.ObjectBox)

	assert.NoErr(t, box.RemoveAll())

	event := iot.Event{
		Device: "my device",
	}
	objectId, err := box.Put(&event)
	assert.NoErr(t, err)
	assert.Eq(t, objectId, event.Id)
	t.Logf("Added object ID %v", objectId)

	event2 := iot.Event{
		Device: "2nd device",
	}
	objectId2, err := box.Put(&event2)
	assert.NoErr(t, err)
	t.Logf("Added 2nd object ID %v", objectId2)

	// read the previous object and compare
	eventRead, err := box.Get(objectId)
	assert.NoErr(t, err)
	assert.Eq(t, event, *eventRead)

	all, err := box.GetAll()
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(all))
	assert.Eq(t, &event, all[0])
	assert.Eq(t, &event2, all[1])
}

func TestBoxInsert(t *testing.T) {
	var env = model.NewTestEnv(t)
	defer env.Close()

	var object = model.Entity47()
	id, err := env.Box.Insert(object)
	assert.NoErr(t, err)
	assert.True(t, id == 1 && object.Id == 1)

	id, err = env.Box.Insert(object)
	assert.Err(t, err)
	assert.True(t, id == 0 && object.Id == 1)
}

func TestBoxUpdate(t *testing.T) {
	var env = model.NewTestEnv(t)
	defer env.Close()

	var object = model.Entity47()

	// update will fail without an ID
	assert.Err(t, env.Box.Update(object))

	// update will also fail with a non-existent ID
	object.Id = 1
	assert.Err(t, env.Box.Update(object))

	object = model.Entity47()
	id, err := env.Box.Insert(object)
	assert.NoErr(t, err)
	assert.True(t, id == 1 && object.Id == 1)

	// finally, update will be successful after we have inserted the object first
	object.String = "foo"
	assert.NoErr(t, env.Box.Update(object))

	objectRead, err := env.Box.Get(id)
	assert.NoErr(t, err)
	assert.Eq(t, object, objectRead)
}

func TestBoxCount(t *testing.T) {
	var env = model.NewTestEnv(t)
	defer env.Close()

	var c = uint64(10)
	env.Populate(uint(c))

	count, err := env.Box.Count()
	assert.NoErr(t, err)
	assert.Eq(t, c, count)

	count, err = env.Box.CountMax(c / 2)
	assert.NoErr(t, err)
	assert.Eq(t, c/2, count)
}

func TestBoxEmpty(t *testing.T) {
	var env = model.NewTestEnv(t)
	defer env.Close()

	isEmpty, err := env.Box.IsEmpty()
	assert.NoErr(t, err)
	assert.Eq(t, true, isEmpty)

	env.Populate(10)

	isEmpty, err = env.Box.IsEmpty()
	assert.NoErr(t, err)
	assert.Eq(t, false, isEmpty)

	assert.NoErr(t, env.Box.RemoveAll())

	isEmpty, err = env.Box.IsEmpty()
	assert.NoErr(t, err)
	assert.Eq(t, true, isEmpty)
}

func TestBoxContains(t *testing.T) {
	var env = model.NewTestEnv(t)
	defer env.Close()

	found, err := env.Box.Contains(1)
	assert.NoErr(t, err)
	assert.Eq(t, false, found)

	env.Populate(1)

	found, err = env.Box.Contains(1)
	assert.NoErr(t, err)
	assert.Eq(t, true, found)

	found, err = env.Box.Contains(2)
	assert.NoErr(t, err)
	assert.Eq(t, false, found)
}

// Includes testing the default string vector (containing 2 normal values and one "")
func TestBoxPutData(t *testing.T) {
	env := model.NewTestEnv(t)
	defer env.Close()

	var inserted = model.Entity47()

	id, err := env.Box.Put(inserted)
	assert.NoErr(t, err)

	read, err := env.Box.Get(id)
	assert.NoErr(t, err)
	assert.Eq(t, inserted, read)
}

func TestBoxPutAndGetStringVectorsEmptyAndNil(t *testing.T) {
	env := model.NewTestEnv(t)
	defer env.Close()

	var inserted = &model.Entity{}

	// not lazy-loaded so it will be an empty slice when read, not nil
	inserted.RelatedSlice = []model.EntityByValue{}
	inserted.Related.NextSlice = []model.EntityByValue{}

	// test empty vectors
	inserted.StringVector = []string{}
	inserted.ByteVector = []byte{}

	id, err := env.Box.Put(inserted)
	assert.NoErr(t, err)

	read, err := env.Box.Get(id)
	assert.NoErr(t, err)
	assert.Eq(t, *inserted, *read)

	// test nil vectors
	inserted.StringVector = nil
	inserted.ByteVector = nil

	id, err = env.Box.Put(inserted)
	assert.NoErr(t, err)

	read, err = env.Box.Get(id)
	assert.NoErr(t, err)
	assert.Eq(t, *inserted, *read)
}

func TestBoxGetMany(t *testing.T) {
	var env = model.NewTestEnv(t)
	defer env.Close()

	env.Populate(1)

	objects, err := env.Box.GetMany(1, 999)
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(objects))
	assert.True(t, objects[0].Id == 1)
	assert.True(t, objects[1] == nil)

	objects, err = env.Box.GetManyExisting(1, 999)
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(objects))
	assert.True(t, objects[0].Id == 1)
}
