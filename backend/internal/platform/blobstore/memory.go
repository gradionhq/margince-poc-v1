// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package blobstore

import (
	"bytes"
	"context"
	"io"
	"sync"
)

// memoryStore is the in-memory Store: the default for unit tests and the
// offline / zero-toolchain dev path. It copies bytes in and out so a
// caller can neither mutate a stored object through a returned reader nor
// have a later write reach into an in-flight read.
type memoryStore struct {
	mu      sync.RWMutex
	objects map[string]memoryObject
}

type memoryObject struct {
	data        []byte
	contentType string
}

// NewMemory returns an in-memory Store. It is safe for concurrent use.
//
//nolint:ireturn // the seam has two providers (memory + s3) behind one Store; returning the interface is the design.
func NewMemory() Store {
	return &memoryStore{objects: make(map[string]memoryObject)}
}

func (m *memoryStore) Put(_ context.Context, key string, r io.Reader, _ int64, contentType string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = memoryObject{data: data, contentType: contentType}
	return nil
}

func (m *memoryStore) Get(_ context.Context, key string) (io.ReadCloser, Object, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	obj, ok := m.objects[key]
	if !ok {
		return nil, Object{}, ErrNotFound
	}
	// Hand back a copy so mutations of the returned bytes never reach the
	// stored object.
	buf := make([]byte, len(obj.data))
	copy(buf, obj.data)
	return io.NopCloser(bytes.NewReader(buf)), objectMeta(key, obj), nil
}

func (m *memoryStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

func (m *memoryStore) Stat(_ context.Context, key string) (Object, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	obj, ok := m.objects[key]
	if !ok {
		return Object{}, ErrNotFound
	}
	return objectMeta(key, obj), nil
}

// Health always succeeds: the in-memory store has no backend to reach.
func (m *memoryStore) Health(_ context.Context) error { return nil }

func objectMeta(key string, obj memoryObject) Object {
	return Object{Key: key, Size: int64(len(obj.data)), ContentType: obj.contentType}
}
