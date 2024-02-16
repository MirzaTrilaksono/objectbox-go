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

package objectbox

/*
#include <stdlib.h>
#include "objectbox.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"unsafe"

	"github.com/google/flatbuffers/go"
)

// Box provides CRUD access to objects of a common type
type Box struct {
	ObjectBox *ObjectBox
	entity    *entity
	cBox      *C.OBX_box
	async     *AsyncBox
}

const defaultSliceCapacity = 16

func newBox(ob *ObjectBox, entityId TypeId) (*Box, error) {
	var box = &Box{
		ObjectBox: ob,
		entity:    ob.getEntityById(entityId),
	}

	if err := cCallBool(func() bool {
		box.cBox = C.obx_box(ob.store, C.obx_schema_id(entityId))
		return box.cBox != nil
	}); err != nil {
		return nil, err
	}

	// NOTE this is different than NewAsyncBox in that it doesn't require explicit closing
	box.async = &AsyncBox{
		box:    box,
		cOwned: false,
	}
	if err := cCallBool(func() bool {
		box.async.cAsync = C.obx_async(box.cBox)
		return box.async.cAsync != nil
	}); err != nil {
		return nil, err
	}

	return box, nil
}

// Async provides access to the default Async Box for asynchronous operations. See AsyncBox for more information.
func (box *Box) Async() *AsyncBox {
	return box.async
}

// Query creates a query with the given conditions. Use generated properties to create conditions.
// Keep the Query object if you intend to execute it multiple times.
// Note: this function panics if you try to create illegal queries; e.g. use properties of an alien type.
// This is typically a programming error. Use QueryOrError instead if you want the explicit error check.
func (box *Box) Query(conditions ...Condition) *Query {
	query, err := box.QueryOrError(conditions...)
	if err != nil {
		panic(fmt.Sprintf("Could not create query - please check your query conditions: %s", err))
	}
	return query
}

// QueryOrError is like Query() but with error handling; e.g. when you build conditions dynamically that may fail.
func (box *Box) QueryOrError(conditions ...Condition) (query *Query, err error) {
	builder := newQueryBuilder(box.ObjectBox, box.entity.id)

	defer func() {
		err2 := builder.Close()
		if err == nil && err2 != nil {
			err = err2
			query = nil
		}
	}()

	if err = builder.applyConditions(conditions); err != nil {
		return nil, err
	}

	query, err = builder.Build(box)

	return // NOTE result might be overwritten by the deferred "closer" function
}

func (box *Box) idForPut(idCandidate uint64) (id uint64, err error) {
	id = uint64(C.obx_box_id_for_put(box.cBox, C.obx_id(idCandidate)))

	if id == 0 { // Perf paranoia: use additional LockOSThread() only if we actually run into an error
		// for native calls/createError()
		runtime.LockOSThread()

		id = uint64(C.obx_box_id_for_put(box.cBox, C.obx_id(idCandidate)))
		if id == 0 {
			err = createError()
		}

		runtime.UnlockOSThread()
	}
	return
}

func (box *Box) idsForPut(count int) (firstId uint64, err error) {
	if count == 0 {
		return 0, nil
	}

	var cFirstID C.obx_id
	if err := cCall(func() C.obx_err {
		return C.obx_box_ids_for_put(box.cBox, C.uint64_t(count), &cFirstID)

	}); err != nil {
		return 0, err
	}

	return uint64(cFirstID), nil
}

func (box *Box) put(object interface{}, alreadyInTx bool, putMode C.OBXPutMode) (id uint64, err error) {
	idFromObject, err := box.entity.binding.GetId(object)
	if err != nil {
		return 0, err
	}

	if putMode == cPutModeUpdate {
		id = idFromObject
		if idFromObject == 0 {
			return 0, errors.New("cannot update an object with ID 0 - if it's a new object use Put or Insert instead")
		}
	} else {
		id, err = box.idForPut(idFromObject)
		if err != nil {
			return 0, err
		}
	}

	// for entities with relations, execute all Put/PutRelated inside a single transaction
	if box.entity.hasRelations && !alreadyInTx {
		err = box.ObjectBox.RunInWriteTx(func() error {
			return box.putOne(id, object, putMode)
		})
	} else {
		err = box.putOne(id, object, putMode)
	}

	// update the id on the object
	if err == nil && idFromObject != id {
		err = box.entity.binding.SetId(object, id)
	}

	if err != nil {
		id = 0
	}

	return id, err
}

func (box *Box) putOne(id uint64, object interface{}, putMode C.OBXPutMode) error {
	if box.entity.hasRelations { // In that case, the caller already ensured to be inside a TX
		if err := box.entity.binding.PutRelated(box.ObjectBox, object, id); err != nil {
			return err
		}
	}

	return box.withObjectBytes(object, id, func(bytes []byte) error {
		return cCall(func() C.obx_err {
			return C.obx_box_put5(box.cBox, C.obx_id(id), unsafe.Pointer(&bytes[0]), C.size_t(len(bytes)), putMode)
		})
	})
}

func (box *Box) withObjectBytes(object interface{}, id uint64, fn func([]byte) error) error {
	var fbb = fbbPool.Get().(*flatbuffers.Builder)

	err := box.entity.binding.Flatten(object, fbb, id)

	if err == nil {
		fbb.Finish(fbb.EndObject())
		err = fn(fbb.FinishedBytes())
	}

	// put the fbb back to the pool for the others to use if it's reasonably small; don't use defer, it's slower
	if cap(fbb.Bytes) < 1024*1024 {
		fbb.Reset()
		fbbPool.Put(fbb)
	}

	return err
}

// PutAsync asynchronously inserts/updates a single object.
// Deprecated: use box.Async().Put() instead
func (box *Box) PutAsync(object interface{}) (id uint64, err error) {
	return box.async.Put(object)
}

// Put synchronously inserts/updates a single object.
// In case the ID is not specified, it would be assigned automatically (auto-increment).
// When inserting, the ID property on the passed object will be assigned the new ID as well.
func (box *Box) Put(object interface{}) (id uint64, err error) {
	return box.put(object, false, cPutModePut)
}

// Insert synchronously inserts a single object.
// As opposed to Put, Insert will fail if an object with the same ID already exists.
// In case the ID is not specified, it would be assigned automatically (auto-increment).
// When inserting, the ID property on the passed object will be assigned the new ID as well.
func (box *Box) Insert(object interface{}) (id uint64, err error) {
	return box.put(object, false, cPutModeInsert)
}

// Update synchronously updates a single object.
// As opposed to Put, Update will fail if an object with the same ID is not found in the database.
func (box *Box) Update(object interface{}) error {
	_, err := box.put(object, false, cPutModeUpdate)
	return err
}

// PutMany inserts multiple objects in a single transaction.
// The given argument must be a slice of the object type this Box represents (pointers to objects).
// In case IDs are not set on the objects, they would be assigned automatically (auto-increment).
//
// Returns: IDs of the put objects (in the same order).
//
// Note: In case an error occurs during the transaction, some of the objects may already have the ID assigned
// even though the transaction has been rolled back and the objects are not stored under those IDs.
//
// Note: The slice may be empty or even nil; in both cases, an empty IDs slice and no error is returned.
func (box *Box) PutMany(objects interface{}) (ids []uint64, err error) {
	var slice = reflect.ValueOf(objects)
	var count = slice.Len()

	// a little optimization for the edge case
	if count == 0 {
		return []uint64{}, nil
	}

	// prepare the result, filled in below
	ids = make([]uint64, count)

	// Execute everything in a single single transaction - for performance and consistency.
	// This is necessary even if count < chunkSize because of relations (PutRelated)
	err = box.ObjectBox.RunInWriteTx(func() error {
		if supportsResultArray {
			// Process the data in chunks so that we don't consume too much memory.
			const chunkSize = 10000 // 10k is the limit currently enforced by obx_box_ids_for_put, maybe make configurable

			var chunks = count / chunkSize
			if count%chunkSize != 0 {
				chunks = chunks + 1
			}

			for c := 0; c < chunks; c++ {
				var start = c * chunkSize
				var end = start + chunkSize
				if end > count {
					end = count
				}

				if err := box.putManyObjects(slice, ids, start, end); err != nil {
					return err
				}
			}
		} else {
			for i := 0; i < count; i++ {
				id, err := box.put(slice.Index(i).Interface(), true, cPutModePut)
				if err != nil {
					return err
				}
				ids[i] = id
			}
		}

		return nil
	})

	if err != nil {
		ids = nil
	}

	return ids, err
}

// putManyObjects inserts a subset of objects, setting their IDs as an outArgument.
// Requires to be called inside a write transaction, i.e. from the ObjectBox.RunInWriteTx() callback.
// The caller of this method (PutMany) already sliced up the data into chunks to mitigate memory consumption.
func (box *Box) putManyObjects(objects reflect.Value, outIds []uint64, start, end int) error {
	var binding = box.entity.binding
	var count = end - start

	// indexes of new objects (zero IDs) in the `outIds` slice
	var indexesNewObjects = make([]int, 0)

	// by default we go with the most efficient way, see the override below
	var putMode = cPutModePutIdGuaranteedToBeNew

	// find out outIds of all the objects & whether they're new objects or updates
	for i := 0; i < count; i++ {
		var index = start + i
		var object = objects.Index(index).Interface()
		if id, err := binding.GetId(object); err != nil {
			return err
		} else if id > 0 {
			outIds[index] = id
			putMode = cPutModePut
		} else {
			indexesNewObjects = append(indexesNewObjects, index)
		}
	}

	// if there are any new objects, reserve IDs for them
	firstNewId, err := box.idsForPut(len(indexesNewObjects))
	if err != nil {
		return err
	}
	for i := 0; i < len(indexesNewObjects); i++ {
		outIds[indexesNewObjects[i]] = firstNewId + uint64(i)
	}

	// flatten all the objects
	var objectsBytes = make([][]byte, count)
	for i := 0; i < count; i++ {
		var key = start + i
		var object = objects.Index(key).Interface()

		// put related entities for the single object
		if box.entity.hasRelations {
			if err := binding.PutRelated(box.ObjectBox, object, outIds[key]); err != nil {
				return err
			}
		}

		// flatten each object to bytes, already with the new ID (if it's an insert)
		if err := box.withObjectBytes(object, outIds[key], func(bytes []byte) error {
			objectsBytes[i] = make([]byte, len(bytes))
			copy(objectsBytes[i], bytes)
			return nil
		}); err != nil {
			return err
		}
	}

	// create a C representation of the objects array
	bytesArray, err := goBytesArrayToC(objectsBytes)
	if err != nil {
		return err
	}
	defer bytesArray.free()

	// only IDs of objects processed in this batch
	idsArray := goUint64ArrayToCObxId(outIds[start:end])

	if err := cCall(func() C.obx_err {
		return C.obx_box_put_many(box.cBox, bytesArray.cBytesArray, idsArray, C.OBXPutMode(putMode))
	}); err != nil {
		return err
	}

	// set IDs on the new objects
	for _, index := range indexesNewObjects {
		if err := binding.SetId(objects.Index(index).Interface(), outIds[index]); err != nil {
			return fmt.Errorf("setting ID on objects[%v] failed: %s", index, err)
		}
	}

	return nil
}

// Remove deletes a single object
func (box *Box) Remove(object interface{}) error {
	id, err := box.entity.binding.GetId(object)
	if err != nil {
		return err
	}

	return box.RemoveId(id)
}

// RemoveId deletes a single object
func (box *Box) RemoveId(id uint64) error {
	return cCall(func() C.obx_err {
		return C.obx_box_remove(box.cBox, C.obx_id(id))
	})
}

// RemoveIds deletes multiple objects at once.
// Returns the number of deleted object or error on failure.
// Note that this method will not fail if an object is not found (e.g. already removed).
// In case you need to strictly check whether all of the objects exist before removing them,
// you can execute multiple box.Contains() and box.Remove() inside a single write transaction.
func (box *Box) RemoveIds(ids ...uint64) (uint64, error) {
	cIds, err := goIdsArrayToC(ids)
	if err != nil {
		return 0, err
	}

	var cResult C.uint64_t
	err = cCall(func() C.obx_err {
		defer cIds.free()
		return C.obx_box_remove_many(box.cBox, cIds.cArray, &cResult)
	})
	return uint64(cResult), err
}

// RemoveAll removes all stored objects.
// This is much faster than removing objects one by one in a loop.
func (box *Box) RemoveAll() error {
	return cCall(func() C.obx_err {
		return C.obx_box_remove_all(box.cBox, nil)
	})
}

// Count returns a number of objects stored
func (box *Box) Count() (uint64, error) {
	return box.CountMax(0)
}

// CountMax returns a number of objects stored (up to a given maximum)
// passing limit=0 is the same as calling Count() - counts all objects without a limit
func (box *Box) CountMax(limit uint64) (uint64, error) {
	var cResult C.uint64_t
	if err := cCall(func() C.obx_err { return C.obx_box_count(box.cBox, C.uint64_t(limit), &cResult) }); err != nil {
		return 0, err
	}
	return uint64(cResult), nil
}

// IsEmpty checks whether the box contains any objects
func (box *Box) IsEmpty() (bool, error) {
	var cResult C.bool
	if err := cCall(func() C.obx_err { return C.obx_box_is_empty(box.cBox, &cResult) }); err != nil {
		return false, err
	}
	return bool(cResult), nil
}

// Get reads a single object.
//
// Returns an interface that should be cast to the appropriate type.
// Returns nil in case the object with the given ID doesn't exist.
// The cast is done automatically when using the generated BoxFor* code.
func (box *Box) Get(id uint64) (object interface{}, err error) {
	// we need a read-transaction to keep the data in dataPtr untouched (by concurrent write) until we can read it
	// as well as making sure the relations read in binding.Load represent a consistent state
	err = box.ObjectBox.RunInReadTx(func() error {
		var data *C.void
		var dataSize C.size_t
		var dataPtr = unsafe.Pointer(data)

		var rc = C.obx_box_get(box.cBox, C.obx_id(id), &dataPtr, &dataSize)
		if rc == 0 {
			var bytes []byte
			cVoidPtrToByteSlice(dataPtr, int(dataSize), &bytes)
			object, err = box.entity.binding.Load(box.ObjectBox, bytes)
			return err
		} else if rc == C.OBX_NOT_FOUND {
			object = nil
			return nil
		} else {
			object = nil
			// NOTE: no need for manual runtime.LockOSThread() because we're inside a read transaction
			return createError()
		}

	})

	return object, err
}

// GetMany reads multiple objects at once.
//
// Returns a slice of objects that should be cast to the appropriate type.
// The cast is done automatically when using the generated BoxFor* code.
// If any of the objects doesn't exist, its position in the return slice
//  is nil or an empty object (depends on the binding)
func (box *Box) GetMany(ids ...uint64) (slice interface{}, err error) {
	const existingOnly = false
	if cIds, err := goIdsArrayToC(ids); err != nil {
		return nil, err
	} else if supportsResultArray {
		defer cIds.free()
		return box.readManyObjects(existingOnly, func() *C.OBX_bytes_array { return C.obx_box_get_many(box.cBox, cIds.cArray) })
	} else {
		var cFn = func(visitorArg unsafe.Pointer) C.obx_err {
			defer cIds.free()
			return C.obx_box_visit_many(box.cBox, cIds.cArray, dataVisitor, visitorArg)
		}
		return box.readUsingVisitor(existingOnly, cFn)
	}
}

// GetManyExisting reads multiple objects at once, skipping those that do not exist.
//
// Returns a slice of objects that should be cast to the appropriate type.
// The cast is done automatically when using the generated BoxFor* code.
func (box *Box) GetManyExisting(ids ...uint64) (slice interface{}, err error) {
	const existingOnly = true
	if cIds, err := goIdsArrayToC(ids); err != nil {
		return nil, err
	} else if supportsResultArray {
		defer cIds.free()
		return box.readManyObjects(existingOnly, func() *C.OBX_bytes_array { return C.obx_box_get_many(box.cBox, cIds.cArray) })
	} else {
		var cFn = func(visitorArg unsafe.Pointer) C.obx_err {
			defer cIds.free()
			return C.obx_box_visit_many(box.cBox, cIds.cArray, dataVisitor, visitorArg)
		}
		return box.readUsingVisitor(existingOnly, cFn)
	}
}

// GetAll reads all stored objects.
//
// Returns a slice of objects that should be cast to the appropriate type.
// The cast is done automatically when using the generated BoxFor* code.
func (box *Box) GetAll() (slice interface{}, err error) {
	const existingOnly = true
	if supportsResultArray {
		return box.readManyObjects(existingOnly, func() *C.OBX_bytes_array { return C.obx_box_get_all(box.cBox) })
	}

	var cFn = func(visitorArg unsafe.Pointer) C.obx_err {
		return C.obx_box_visit_all(box.cBox, dataVisitor, visitorArg)
	}
	return box.readUsingVisitor(existingOnly, cFn)
}

func (box *Box) readManyObjects(existingOnly bool, cFn func() *C.OBX_bytes_array) (slice interface{}, err error) {
	// we need a read-transaction to keep the data in dataPtr untouched (by concurrent write) until we can read it
	// as well as making sure the relations read in binding.Load represent a consistent state
	err = box.ObjectBox.RunInReadTx(func() error {
		bytesArray, err := cGetBytesArray(cFn)
		if err != nil {
			return err
		}

		var binding = box.entity.binding
		slice = binding.MakeSlice(len(bytesArray))
		for _, bytesData := range bytesArray {
			if bytesData == nil {
				// may be nil if an object on this index was not found (can happen with GetMany)
				if !existingOnly {
					slice = binding.AppendToSlice(slice, nil)
				}
				continue
			}

			object, err := binding.Load(box.ObjectBox, bytesData)
			if err != nil {
				return err
			}
			slice = binding.AppendToSlice(slice, object)
		}
		return nil
	})

	if err != nil {
		slice = nil
	}

	return slice, err
}

// this is a utility function to fetch objects using an obx_data_visitor
func (box *Box) readUsingVisitor(existingOnly bool, cFn func(visitorArg unsafe.Pointer) C.obx_err) (slice interface{}, err error) {
	var binding = box.entity.binding
	var visitor uint32
	visitor, err = dataVisitorRegister(func(bytes []byte) bool {
		// may be nil if an object on this index was not found (can happen with GetMany)
		if bytes == nil {
			if !existingOnly {
				slice = binding.AppendToSlice(slice, nil)
			}
			return true
		}

		object, err2 := binding.Load(box.ObjectBox, bytes)
		if err2 != nil {
			err = err2
			return false
		}
		slice = binding.AppendToSlice(slice, object)
		return true
	})
	if err != nil {
		return nil, err
	}
	defer dataVisitorUnregister(visitor)

	slice = binding.MakeSlice(defaultSliceCapacity)

	// we need a read-transaction to keep the data in dataPtr untouched (by concurrent write) until we can read it
	// as well as making sure the relations read in binding.Load represent a consistent state
	// use another `error` variable as `err` may be set by the visitor callback above
	var err2 = box.ObjectBox.RunInReadTx(func() error {
		return cCall(func() C.obx_err { return cFn(unsafe.Pointer(&visitor)) })
	})

	if err2 != nil {
		return nil, err2
	} else if err != nil {
		return nil, err
	} else {
		return slice, nil
	}
}

// Contains checks whether an object with the given ID is stored.
func (box *Box) Contains(id uint64) (bool, error) {
	var cResult C.bool
	if err := cCall(func() C.obx_err { return C.obx_box_contains(box.cBox, C.obx_id(id), &cResult) }); err != nil {
		return false, err
	}
	return bool(cResult), nil
}

// ContainsIds checks whether all of the given objects are stored in DB.
func (box *Box) ContainsIds(ids ...uint64) (bool, error) {
	cIds, err := goIdsArrayToC(ids)
	if err != nil {
		return false, err
	}

	var cResult C.bool
	err = cCall(func() C.obx_err {
		defer cIds.free()
		return C.obx_box_contains_many(box.cBox, cIds.cArray, &cResult)
	})
	return bool(cResult), err
}

// RelationIds returns IDs of all target objects related to the given source object ID
func (box *Box) RelationIds(relation *RelationToMany, sourceId uint64) ([]uint64, error) {
	targetBox, err := box.ObjectBox.box(relation.Target.Id)
	if err != nil {
		return nil, err
	}
	return cGetIds(func() *C.OBX_id_array {
		return C.obx_box_rel_get_ids(targetBox.cBox, C.obx_schema_id(relation.Id), C.obx_id(sourceId))
	})
}

// RelationReplace replaces all targets for a given source in a standalone many-to-many relation
// It also inserts new related objects (with a 0 ID).
func (box *Box) RelationReplace(relation *RelationToMany, sourceId uint64, sourceObject interface{},
	targetObjects interface{}) error {

	// get id from the object, if inserting, it would be 0 even if the argument id is already non-zero
	// this saves us an unnecessary request to RelationIds for new objects (there can't be any relations yet)
	id, err := box.entity.binding.GetId(sourceObject)
	if err != nil {
		return err
	}

	sliceValue := reflect.ValueOf(targetObjects)

	// If the slice was nil it would be handled as an empty slice and removed all relations.
	// This would cause problems with lazy-loaded relations during update, if GetRelated wasn't called.
	// Therefore, we preemptively prevent such updates and force users to explicitly pass an empty slice instead.
	if sliceValue.IsNil() && id != 0 {
		return fmt.Errorf("given NIL instead of an empty slice of target objects for relation ID %v - "+
			"this is forbidden for updates due to potential code logic problems you may encounter when using "+
			"lazy-loaded relations; pass an empty slice if you really want to remove all related entities", relation.Id)
	}

	count := sliceValue.Len()

	// make a map of related target entity IDs, marking those that were originally related but should be removed
	var idsToRemove = make(map[uint64]bool)

	return box.ObjectBox.RunInWriteTx(func() error {
		if id != 0 {
			oldRelIds, err := box.RelationIds(relation, sourceId)
			if err != nil {
				return err
			}
			for _, rId := range oldRelIds {
				idsToRemove[rId] = true
			}
		}

		if count > 0 {
			var targetBox = box.ObjectBox.InternalBox(relation.Target.Id)

			// walk over the current related objects, mark those that still exist, add the new ones
			for i := 0; i < count; i++ {
				var reflObj = sliceValue.Index(i)
				var rel interface{}
				if reflObj.Kind() == reflect.Ptr {
					rel = reflObj.Interface()
				} else {
					rel = reflObj.Addr().Interface()
				}

				rId, err := targetBox.entity.binding.GetId(rel)
				if err != nil {
					return err
				} else if rId == 0 {
					if rId, err = targetBox.Put(rel); err != nil {
						return err
					}
				}

				if idsToRemove[rId] {
					// old relation that still exists, keep it
					delete(idsToRemove, rId)
				} else {
					// new relation, add it
					if err := box.RelationPut(relation, sourceId, rId); err != nil {
						return err
					}
				}
			}
		}

		// remove those that were not found in the rSlice but were originally related to this entity
		for rId := range idsToRemove {
			if err := box.RelationRemove(relation, sourceId, rId); err != nil {
				return err
			}
		}

		return nil
	})
}

// RelationPut creates a relation between the given source & target objects
func (box *Box) RelationPut(relation *RelationToMany, sourceId, targetId uint64) error {
	return cCall(func() C.obx_err {
		return C.obx_box_rel_put(box.cBox, C.obx_schema_id(relation.Id), C.obx_id(sourceId), C.obx_id(targetId))
	})
}

// RelationRemove removes a relation between the given source & target objects
func (box *Box) RelationRemove(relation *RelationToMany, sourceId, targetId uint64) error {
	return cCall(func() C.obx_err {
		return C.obx_box_rel_remove(box.cBox, C.obx_schema_id(relation.Id), C.obx_id(sourceId), C.obx_id(targetId))
	})
}
