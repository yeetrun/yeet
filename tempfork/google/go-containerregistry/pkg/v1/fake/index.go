// Code generated by counterfeiter. DO NOT EDIT.
package fake

import (
	"sync"

	v1 "github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/types"
)

type FakeImageIndex struct {
	DigestStub        func() (v1.Hash, error)
	digestMutex       sync.RWMutex
	digestArgsForCall []struct {
	}
	digestReturns struct {
		result1 v1.Hash
		result2 error
	}
	digestReturnsOnCall map[int]struct {
		result1 v1.Hash
		result2 error
	}
	ImageStub        func(v1.Hash) (v1.Image, error)
	imageMutex       sync.RWMutex
	imageArgsForCall []struct {
		arg1 v1.Hash
	}
	imageReturns struct {
		result1 v1.Image
		result2 error
	}
	imageReturnsOnCall map[int]struct {
		result1 v1.Image
		result2 error
	}
	ImageIndexStub        func(v1.Hash) (v1.ImageIndex, error)
	imageIndexMutex       sync.RWMutex
	imageIndexArgsForCall []struct {
		arg1 v1.Hash
	}
	imageIndexReturns struct {
		result1 v1.ImageIndex
		result2 error
	}
	imageIndexReturnsOnCall map[int]struct {
		result1 v1.ImageIndex
		result2 error
	}
	IndexManifestStub        func() (*v1.IndexManifest, error)
	indexManifestMutex       sync.RWMutex
	indexManifestArgsForCall []struct {
	}
	indexManifestReturns struct {
		result1 *v1.IndexManifest
		result2 error
	}
	indexManifestReturnsOnCall map[int]struct {
		result1 *v1.IndexManifest
		result2 error
	}
	MediaTypeStub        func() (types.MediaType, error)
	mediaTypeMutex       sync.RWMutex
	mediaTypeArgsForCall []struct {
	}
	mediaTypeReturns struct {
		result1 types.MediaType
		result2 error
	}
	mediaTypeReturnsOnCall map[int]struct {
		result1 types.MediaType
		result2 error
	}
	RawManifestStub        func() ([]byte, error)
	rawManifestMutex       sync.RWMutex
	rawManifestArgsForCall []struct {
	}
	rawManifestReturns struct {
		result1 []byte
		result2 error
	}
	rawManifestReturnsOnCall map[int]struct {
		result1 []byte
		result2 error
	}
	SizeStub        func() (int64, error)
	sizeMutex       sync.RWMutex
	sizeArgsForCall []struct {
	}
	sizeReturns struct {
		result1 int64
		result2 error
	}
	sizeReturnsOnCall map[int]struct {
		result1 int64
		result2 error
	}
	invocations      map[string][][]interface{}
	invocationsMutex sync.RWMutex
}

func (fake *FakeImageIndex) Digest() (v1.Hash, error) {
	fake.digestMutex.Lock()
	ret, specificReturn := fake.digestReturnsOnCall[len(fake.digestArgsForCall)]
	fake.digestArgsForCall = append(fake.digestArgsForCall, struct {
	}{})
	stub := fake.DigestStub
	fakeReturns := fake.digestReturns
	fake.recordInvocation("Digest", []interface{}{})
	fake.digestMutex.Unlock()
	if stub != nil {
		return stub()
	}
	if specificReturn {
		return ret.result1, ret.result2
	}
	return fakeReturns.result1, fakeReturns.result2
}

func (fake *FakeImageIndex) DigestCallCount() int {
	fake.digestMutex.RLock()
	defer fake.digestMutex.RUnlock()
	return len(fake.digestArgsForCall)
}

func (fake *FakeImageIndex) DigestCalls(stub func() (v1.Hash, error)) {
	fake.digestMutex.Lock()
	defer fake.digestMutex.Unlock()
	fake.DigestStub = stub
}

func (fake *FakeImageIndex) DigestReturns(result1 v1.Hash, result2 error) {
	fake.digestMutex.Lock()
	defer fake.digestMutex.Unlock()
	fake.DigestStub = nil
	fake.digestReturns = struct {
		result1 v1.Hash
		result2 error
	}{result1, result2}
}

func (fake *FakeImageIndex) DigestReturnsOnCall(i int, result1 v1.Hash, result2 error) {
	fake.digestMutex.Lock()
	defer fake.digestMutex.Unlock()
	fake.DigestStub = nil
	if fake.digestReturnsOnCall == nil {
		fake.digestReturnsOnCall = make(map[int]struct {
			result1 v1.Hash
			result2 error
		})
	}
	fake.digestReturnsOnCall[i] = struct {
		result1 v1.Hash
		result2 error
	}{result1, result2}
}

func (fake *FakeImageIndex) Image(arg1 v1.Hash) (v1.Image, error) {
	fake.imageMutex.Lock()
	ret, specificReturn := fake.imageReturnsOnCall[len(fake.imageArgsForCall)]
	fake.imageArgsForCall = append(fake.imageArgsForCall, struct {
		arg1 v1.Hash
	}{arg1})
	stub := fake.ImageStub
	fakeReturns := fake.imageReturns
	fake.recordInvocation("Image", []interface{}{arg1})
	fake.imageMutex.Unlock()
	if stub != nil {
		return stub(arg1)
	}
	if specificReturn {
		return ret.result1, ret.result2
	}
	return fakeReturns.result1, fakeReturns.result2
}

func (fake *FakeImageIndex) ImageCallCount() int {
	fake.imageMutex.RLock()
	defer fake.imageMutex.RUnlock()
	return len(fake.imageArgsForCall)
}

func (fake *FakeImageIndex) ImageCalls(stub func(v1.Hash) (v1.Image, error)) {
	fake.imageMutex.Lock()
	defer fake.imageMutex.Unlock()
	fake.ImageStub = stub
}

func (fake *FakeImageIndex) ImageArgsForCall(i int) v1.Hash {
	fake.imageMutex.RLock()
	defer fake.imageMutex.RUnlock()
	argsForCall := fake.imageArgsForCall[i]
	return argsForCall.arg1
}

func (fake *FakeImageIndex) ImageReturns(result1 v1.Image, result2 error) {
	fake.imageMutex.Lock()
	defer fake.imageMutex.Unlock()
	fake.ImageStub = nil
	fake.imageReturns = struct {
		result1 v1.Image
		result2 error
	}{result1, result2}
}

func (fake *FakeImageIndex) ImageReturnsOnCall(i int, result1 v1.Image, result2 error) {
	fake.imageMutex.Lock()
	defer fake.imageMutex.Unlock()
	fake.ImageStub = nil
	if fake.imageReturnsOnCall == nil {
		fake.imageReturnsOnCall = make(map[int]struct {
			result1 v1.Image
			result2 error
		})
	}
	fake.imageReturnsOnCall[i] = struct {
		result1 v1.Image
		result2 error
	}{result1, result2}
}

func (fake *FakeImageIndex) ImageIndex(arg1 v1.Hash) (v1.ImageIndex, error) {
	fake.imageIndexMutex.Lock()
	ret, specificReturn := fake.imageIndexReturnsOnCall[len(fake.imageIndexArgsForCall)]
	fake.imageIndexArgsForCall = append(fake.imageIndexArgsForCall, struct {
		arg1 v1.Hash
	}{arg1})
	stub := fake.ImageIndexStub
	fakeReturns := fake.imageIndexReturns
	fake.recordInvocation("ImageIndex", []interface{}{arg1})
	fake.imageIndexMutex.Unlock()
	if stub != nil {
		return stub(arg1)
	}
	if specificReturn {
		return ret.result1, ret.result2
	}
	return fakeReturns.result1, fakeReturns.result2
}

func (fake *FakeImageIndex) ImageIndexCallCount() int {
	fake.imageIndexMutex.RLock()
	defer fake.imageIndexMutex.RUnlock()
	return len(fake.imageIndexArgsForCall)
}

func (fake *FakeImageIndex) ImageIndexCalls(stub func(v1.Hash) (v1.ImageIndex, error)) {
	fake.imageIndexMutex.Lock()
	defer fake.imageIndexMutex.Unlock()
	fake.ImageIndexStub = stub
}

func (fake *FakeImageIndex) ImageIndexArgsForCall(i int) v1.Hash {
	fake.imageIndexMutex.RLock()
	defer fake.imageIndexMutex.RUnlock()
	argsForCall := fake.imageIndexArgsForCall[i]
	return argsForCall.arg1
}

func (fake *FakeImageIndex) ImageIndexReturns(result1 v1.ImageIndex, result2 error) {
	fake.imageIndexMutex.Lock()
	defer fake.imageIndexMutex.Unlock()
	fake.ImageIndexStub = nil
	fake.imageIndexReturns = struct {
		result1 v1.ImageIndex
		result2 error
	}{result1, result2}
}

func (fake *FakeImageIndex) ImageIndexReturnsOnCall(i int, result1 v1.ImageIndex, result2 error) {
	fake.imageIndexMutex.Lock()
	defer fake.imageIndexMutex.Unlock()
	fake.ImageIndexStub = nil
	if fake.imageIndexReturnsOnCall == nil {
		fake.imageIndexReturnsOnCall = make(map[int]struct {
			result1 v1.ImageIndex
			result2 error
		})
	}
	fake.imageIndexReturnsOnCall[i] = struct {
		result1 v1.ImageIndex
		result2 error
	}{result1, result2}
}

func (fake *FakeImageIndex) IndexManifest() (*v1.IndexManifest, error) {
	fake.indexManifestMutex.Lock()
	ret, specificReturn := fake.indexManifestReturnsOnCall[len(fake.indexManifestArgsForCall)]
	fake.indexManifestArgsForCall = append(fake.indexManifestArgsForCall, struct {
	}{})
	stub := fake.IndexManifestStub
	fakeReturns := fake.indexManifestReturns
	fake.recordInvocation("IndexManifest", []interface{}{})
	fake.indexManifestMutex.Unlock()
	if stub != nil {
		return stub()
	}
	if specificReturn {
		return ret.result1, ret.result2
	}
	return fakeReturns.result1, fakeReturns.result2
}

func (fake *FakeImageIndex) IndexManifestCallCount() int {
	fake.indexManifestMutex.RLock()
	defer fake.indexManifestMutex.RUnlock()
	return len(fake.indexManifestArgsForCall)
}

func (fake *FakeImageIndex) IndexManifestCalls(stub func() (*v1.IndexManifest, error)) {
	fake.indexManifestMutex.Lock()
	defer fake.indexManifestMutex.Unlock()
	fake.IndexManifestStub = stub
}

func (fake *FakeImageIndex) IndexManifestReturns(result1 *v1.IndexManifest, result2 error) {
	fake.indexManifestMutex.Lock()
	defer fake.indexManifestMutex.Unlock()
	fake.IndexManifestStub = nil
	fake.indexManifestReturns = struct {
		result1 *v1.IndexManifest
		result2 error
	}{result1, result2}
}

func (fake *FakeImageIndex) IndexManifestReturnsOnCall(i int, result1 *v1.IndexManifest, result2 error) {
	fake.indexManifestMutex.Lock()
	defer fake.indexManifestMutex.Unlock()
	fake.IndexManifestStub = nil
	if fake.indexManifestReturnsOnCall == nil {
		fake.indexManifestReturnsOnCall = make(map[int]struct {
			result1 *v1.IndexManifest
			result2 error
		})
	}
	fake.indexManifestReturnsOnCall[i] = struct {
		result1 *v1.IndexManifest
		result2 error
	}{result1, result2}
}

func (fake *FakeImageIndex) MediaType() (types.MediaType, error) {
	fake.mediaTypeMutex.Lock()
	ret, specificReturn := fake.mediaTypeReturnsOnCall[len(fake.mediaTypeArgsForCall)]
	fake.mediaTypeArgsForCall = append(fake.mediaTypeArgsForCall, struct {
	}{})
	stub := fake.MediaTypeStub
	fakeReturns := fake.mediaTypeReturns
	fake.recordInvocation("MediaType", []interface{}{})
	fake.mediaTypeMutex.Unlock()
	if stub != nil {
		return stub()
	}
	if specificReturn {
		return ret.result1, ret.result2
	}
	return fakeReturns.result1, fakeReturns.result2
}

func (fake *FakeImageIndex) MediaTypeCallCount() int {
	fake.mediaTypeMutex.RLock()
	defer fake.mediaTypeMutex.RUnlock()
	return len(fake.mediaTypeArgsForCall)
}

func (fake *FakeImageIndex) MediaTypeCalls(stub func() (types.MediaType, error)) {
	fake.mediaTypeMutex.Lock()
	defer fake.mediaTypeMutex.Unlock()
	fake.MediaTypeStub = stub
}

func (fake *FakeImageIndex) MediaTypeReturns(result1 types.MediaType, result2 error) {
	fake.mediaTypeMutex.Lock()
	defer fake.mediaTypeMutex.Unlock()
	fake.MediaTypeStub = nil
	fake.mediaTypeReturns = struct {
		result1 types.MediaType
		result2 error
	}{result1, result2}
}

func (fake *FakeImageIndex) MediaTypeReturnsOnCall(i int, result1 types.MediaType, result2 error) {
	fake.mediaTypeMutex.Lock()
	defer fake.mediaTypeMutex.Unlock()
	fake.MediaTypeStub = nil
	if fake.mediaTypeReturnsOnCall == nil {
		fake.mediaTypeReturnsOnCall = make(map[int]struct {
			result1 types.MediaType
			result2 error
		})
	}
	fake.mediaTypeReturnsOnCall[i] = struct {
		result1 types.MediaType
		result2 error
	}{result1, result2}
}

func (fake *FakeImageIndex) RawManifest() ([]byte, error) {
	fake.rawManifestMutex.Lock()
	ret, specificReturn := fake.rawManifestReturnsOnCall[len(fake.rawManifestArgsForCall)]
	fake.rawManifestArgsForCall = append(fake.rawManifestArgsForCall, struct {
	}{})
	stub := fake.RawManifestStub
	fakeReturns := fake.rawManifestReturns
	fake.recordInvocation("RawManifest", []interface{}{})
	fake.rawManifestMutex.Unlock()
	if stub != nil {
		return stub()
	}
	if specificReturn {
		return ret.result1, ret.result2
	}
	return fakeReturns.result1, fakeReturns.result2
}

func (fake *FakeImageIndex) RawManifestCallCount() int {
	fake.rawManifestMutex.RLock()
	defer fake.rawManifestMutex.RUnlock()
	return len(fake.rawManifestArgsForCall)
}

func (fake *FakeImageIndex) RawManifestCalls(stub func() ([]byte, error)) {
	fake.rawManifestMutex.Lock()
	defer fake.rawManifestMutex.Unlock()
	fake.RawManifestStub = stub
}

func (fake *FakeImageIndex) RawManifestReturns(result1 []byte, result2 error) {
	fake.rawManifestMutex.Lock()
	defer fake.rawManifestMutex.Unlock()
	fake.RawManifestStub = nil
	fake.rawManifestReturns = struct {
		result1 []byte
		result2 error
	}{result1, result2}
}

func (fake *FakeImageIndex) RawManifestReturnsOnCall(i int, result1 []byte, result2 error) {
	fake.rawManifestMutex.Lock()
	defer fake.rawManifestMutex.Unlock()
	fake.RawManifestStub = nil
	if fake.rawManifestReturnsOnCall == nil {
		fake.rawManifestReturnsOnCall = make(map[int]struct {
			result1 []byte
			result2 error
		})
	}
	fake.rawManifestReturnsOnCall[i] = struct {
		result1 []byte
		result2 error
	}{result1, result2}
}

func (fake *FakeImageIndex) Size() (int64, error) {
	fake.sizeMutex.Lock()
	ret, specificReturn := fake.sizeReturnsOnCall[len(fake.sizeArgsForCall)]
	fake.sizeArgsForCall = append(fake.sizeArgsForCall, struct {
	}{})
	stub := fake.SizeStub
	fakeReturns := fake.sizeReturns
	fake.recordInvocation("Size", []interface{}{})
	fake.sizeMutex.Unlock()
	if stub != nil {
		return stub()
	}
	if specificReturn {
		return ret.result1, ret.result2
	}
	return fakeReturns.result1, fakeReturns.result2
}

func (fake *FakeImageIndex) SizeCallCount() int {
	fake.sizeMutex.RLock()
	defer fake.sizeMutex.RUnlock()
	return len(fake.sizeArgsForCall)
}

func (fake *FakeImageIndex) SizeCalls(stub func() (int64, error)) {
	fake.sizeMutex.Lock()
	defer fake.sizeMutex.Unlock()
	fake.SizeStub = stub
}

func (fake *FakeImageIndex) SizeReturns(result1 int64, result2 error) {
	fake.sizeMutex.Lock()
	defer fake.sizeMutex.Unlock()
	fake.SizeStub = nil
	fake.sizeReturns = struct {
		result1 int64
		result2 error
	}{result1, result2}
}

func (fake *FakeImageIndex) SizeReturnsOnCall(i int, result1 int64, result2 error) {
	fake.sizeMutex.Lock()
	defer fake.sizeMutex.Unlock()
	fake.SizeStub = nil
	if fake.sizeReturnsOnCall == nil {
		fake.sizeReturnsOnCall = make(map[int]struct {
			result1 int64
			result2 error
		})
	}
	fake.sizeReturnsOnCall[i] = struct {
		result1 int64
		result2 error
	}{result1, result2}
}

func (fake *FakeImageIndex) Invocations() map[string][][]interface{} {
	fake.invocationsMutex.RLock()
	defer fake.invocationsMutex.RUnlock()
	fake.digestMutex.RLock()
	defer fake.digestMutex.RUnlock()
	fake.imageMutex.RLock()
	defer fake.imageMutex.RUnlock()
	fake.imageIndexMutex.RLock()
	defer fake.imageIndexMutex.RUnlock()
	fake.indexManifestMutex.RLock()
	defer fake.indexManifestMutex.RUnlock()
	fake.mediaTypeMutex.RLock()
	defer fake.mediaTypeMutex.RUnlock()
	fake.rawManifestMutex.RLock()
	defer fake.rawManifestMutex.RUnlock()
	fake.sizeMutex.RLock()
	defer fake.sizeMutex.RUnlock()
	copiedInvocations := map[string][][]interface{}{}
	for key, value := range fake.invocations {
		copiedInvocations[key] = value
	}
	return copiedInvocations
}

func (fake *FakeImageIndex) recordInvocation(key string, args []interface{}) {
	fake.invocationsMutex.Lock()
	defer fake.invocationsMutex.Unlock()
	if fake.invocations == nil {
		fake.invocations = map[string][][]interface{}{}
	}
	if fake.invocations[key] == nil {
		fake.invocations[key] = [][]interface{}{}
	}
	fake.invocations[key] = append(fake.invocations[key], args)
}

var _ v1.ImageIndex = new(FakeImageIndex)
