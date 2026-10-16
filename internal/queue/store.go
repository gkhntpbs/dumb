package queue

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

const DefaultStateMaxBytes int64 = 256 << 20
const storeOverhead int64 = 4096

var ErrStorage = errors.New("queue storage unavailable")
var ErrStorageCapacity = errors.New("queue storage capacity exhausted")
var stateName = regexp.MustCompile(`^job-([a-f0-9]{32})\.bin$`)
var tempName = regexp.MustCompile(`^\.job-[a-f0-9]{32}\.tmp$`)

type StoreConfig struct {
	Directory string
	MaxBytes  int64
}

type storedJob struct {
	Thinking, DoneReason                           string
	ThinkSet, ThinkValue                           bool
	Version                                        int
	ID, Owner, Key                                 string
	Digest                                         [32]byte
	Request                                        protocol.SubmitRequest
	Created, Leased, Deadline, RetainUntil, AckDue time.Time
	State, LeaseID, WorkerID, Output, Error        string
	Acked                                          bool
	Order                                          int64
	Allocation                                     int64
}

type storeFile struct{ size, charge int64 }

type stateStore struct {
	root          *os.Root
	lock          *os.File
	limit, used   int64
	maxRecord     int64
	files         map[string]storeFile
	poisoned      bool
	write         func(*os.File, []byte) (int, error)
	sync          func(*os.File) error
	rename        func(string, string) error
	syncDirectory func() error
	remove        func(string) error
}

func recordOf(j *job) storedJob {
	record := storedJob{Allocation: j.diskCharge, Version: 1, ID: j.id, Owner: j.owner, Key: j.key, Digest: j.digest, Request: j.req, Created: j.created, Leased: j.leased, Deadline: j.deadline, RetainUntil: j.retainUntil, AckDue: j.ackDue, State: j.state, LeaseID: j.leaseID, WorkerID: j.workerID, Output: j.output, Error: j.err, Thinking: j.thinking, DoneReason: j.doneReason, Acked: j.acked, Order: j.order}
	if j.req.Think != nil {
		record.ThinkSet = true
		record.ThinkValue = *j.req.Think
	}
	return record
}

func encodeRecord(r storedJob) ([]byte, error) {
	var payload bytes.Buffer
	if err := gob.NewEncoder(&payload).Encode(r); err != nil {
		return nil, err
	}
	data := make([]byte, 44+payload.Len())
	copy(data, "DUMBQ001")
	binary.BigEndian.PutUint32(data[8:12], uint32(payload.Len()))
	sum := sha256.Sum256(payload.Bytes())
	copy(data[12:44], sum[:])
	copy(data[44:], payload.Bytes())
	return data, nil
}

func decodeRecord(data []byte) (storedJob, error) {
	var r storedJob
	if len(data) < 44 || string(data[:8]) != "DUMBQ001" || uint64(binary.BigEndian.Uint32(data[8:12])) != uint64(len(data)-44) {
		return r, ErrStorage
	}
	sum := sha256.Sum256(data[44:])
	if !bytes.Equal(sum[:], data[12:44]) {
		return r, ErrStorage
	}
	decoder := gob.NewDecoder(bytes.NewReader(data[44:]))
	if err := decoder.Decode(&r); err != nil {
		return r, ErrStorage
	}
	if !r.ThinkSet && (r.Request.Think != nil || r.ThinkValue) {
		return r, ErrStorage
	}
	if r.ThinkSet {
		r.Request.Think = cloneThink(&r.ThinkValue)
	} else {
		r.Request.Think = nil
	}
	var extra storedJob
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return r, ErrStorage
	}
	if r.Allocation < 0 || r.Version != 1 || len(r.ID) != 32 || r.Owner == "" || r.Request.Model == "" || r.Created.IsZero() || r.Deadline.Before(r.Created) || r.Order == 0 || r.Order == int64(-1<<63) || r.Order == int64(1<<63-1) {
		return r, ErrStorage
	}
	if _, err := hex.DecodeString(r.ID); err != nil {
		return r, ErrStorage
	}
	if r.Key != "" && !idempotencyKey.MatchString(r.Key) {
		return r, ErrStorage
	}
	if r.Request.DeadlineMS < 1 || r.Request.DeadlineMS > protocol.MaxDeadlineMS || r.Deadline.Sub(r.Created) != time.Duration(r.Request.DeadlineMS)*time.Millisecond || r.Request.NumPredict < 0 || r.Request.NumPredict > protocol.MaxNumPredict || (r.Request.Format != "" && r.Request.Format != "json") {
		return r, ErrStorage
	}
	if r.Key != "" && r.Digest == ([32]byte{}) {
		return r, ErrStorage
	}
	if (r.State == protocol.StateQueued || r.State == protocol.StateLeased) && (r.Output != "" || r.Error != "" || r.Thinking != "" || r.DoneReason != "" || r.Key != "" && r.Digest != payloadDigest(r.Request)) {
		return r, ErrStorage
	}
	if r.State == protocol.StateDone && r.Error != "" || r.State == protocol.StateFailed && (r.Error == "" || r.Output != "" || r.Thinking != "" || r.DoneReason != "") || (r.State == protocol.StateExpired || r.State == protocol.StateCancelled) && (r.Output != "" || r.Error != "" || r.Thinking != "" || r.DoneReason != "") {
		return r, ErrStorage
	}
	if !r.RetainUntil.IsZero() && (r.RetainUntil.After(r.Deadline.Add(Retention)) || r.RetainUntil.Before(r.Created.Add(Retention))) {
		return r, ErrStorage
	}
	if r.State == protocol.StateExpired && !r.RetainUntil.Equal(r.Deadline.Add(Retention)) {
		return r, ErrStorage
	}
	if _, fits := boundedRequestBytes(MaxResultBytes, r.Output, r.Error, r.Thinking, r.DoneReason); !fits {
		return r, ErrStorage
	}
	switch r.State {
	case protocol.StateQueued:
		if r.Request.Prompt == "" || r.LeaseID != "" || r.WorkerID != "" || !r.RetainUntil.IsZero() || !r.AckDue.IsZero() || r.Acked {
			return r, ErrStorage
		}
	case protocol.StateLeased:
		if _, err := hex.DecodeString(r.LeaseID); err != nil {
			return r, ErrStorage
		}
		if r.Request.Prompt == "" || len(r.LeaseID) != 32 || r.WorkerID == "" || r.Leased.Before(r.Created) || !r.Leased.Before(r.Deadline) || !r.RetainUntil.IsZero() || r.AckDue.After(r.Deadline) || !r.AckDue.IsZero() && r.AckDue.Before(r.Leased) {
			return r, ErrStorage
		}
	case protocol.StateDone, protocol.StateFailed, protocol.StateExpired, protocol.StateCancelled:
		if r.Allocation != 0 {
			return r, ErrStorage
		}
		if r.RetainUntil.IsZero() || r.Request.Prompt != "" || r.Request.System != "" || r.Request.Format != "" || r.Request.NumPredict != 0 || r.Request.Think != nil {
			return r, ErrStorage
		}
	default:
		return r, ErrStorage
	}
	return r, nil
}

func openStateStore(cfg StoreConfig, maxJobs int, maxBytes int) (*stateStore, []storedJob, error) {
	if cfg.Directory == "" {
		if cfg.MaxBytes != 0 {
			return nil, nil, errors.New("queue state directory is required when a disk limit is configured")
		}
		return nil, nil, nil
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, nil, errors.New("durable queue storage requires Linux or macOS")
	}
	if !filepath.IsAbs(cfg.Directory) || filepath.Clean(cfg.Directory) != cfg.Directory {
		return nil, nil, errors.New("queue state directory must be an absolute clean path")
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = DefaultStateMaxBytes
	}
	if cfg.MaxBytes <= int64(MaxResultBytes)+storeOverhead {
		return nil, nil, errors.New("queue state byte limit must exceed one result reservation and metadata")
	}
	if err := os.Mkdir(cfg.Directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, nil, err
	}
	info, err := os.Lstat(cfg.Directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, nil, errors.New("queue state directory must be a private regular directory with mode 0700")
	}
	if err = validateStoreOwner(info); err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(cfg.Directory)
	if err != nil {
		return nil, nil, err
	}
	s := &stateStore{root: root, limit: cfg.MaxBytes, maxRecord: min(int64(maxBytes), cfg.MaxBytes-storeOverhead) + storeOverhead, files: map[string]storeFile{}}
	fail := func(err error) (*stateStore, []storedJob, error) { s.close(); return nil, nil, err }
	if info, err = root.Lstat("store.lock"); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return fail(ErrStorage)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	s.lock, err = root.OpenFile("store.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fail(err)
	}
	if err = lockStateFile(s.lock); err != nil {
		return fail(err)
	}
	s.write = func(f *os.File, b []byte) (int, error) { return f.Write(b) }
	s.sync = func(f *os.File) error { return f.Sync() }
	s.rename = func(a, b string) error { return root.Rename(a, b) }
	s.remove = func(name string) error { return root.Remove(name) }
	s.syncDirectory = func() error {
		f, err := root.Open(".")
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Sync()
	}
	if err = s.syncDirectory(); err != nil {
		return fail(err)
	}
	parent, err := os.Open(filepath.Dir(cfg.Directory))
	if err != nil {
		return fail(err)
	}
	err = parent.Sync()
	closeErr := parent.Close()
	if err != nil {
		return fail(err)
	}
	if closeErr != nil {
		return fail(closeErr)
	}
	directory, err := root.Open(".")
	if err != nil {
		return fail(err)
	}
	entries, err := directory.ReadDir(maxJobs + 3)
	directory.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return fail(err)
	}
	if len(entries) > maxJobs+2 {
		return fail(errors.New("queue state directory exceeds configured job count"))
	}
	records := []storedJob{}
	for _, entry := range entries {
		name := entry.Name()
		info, err := root.Lstat(name)
		if err != nil {
			return fail(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return fail(ErrStorage)
		}
		if err = validateStoreOwner(info); err != nil {
			return fail(err)
		}
		if name == "store.lock" {
			continue
		}
		if tempName.MatchString(name) {
			if err = s.remove(name); err != nil {
				return fail(err)
			}
			continue
		}
		matches := stateName.FindStringSubmatch(name)
		if matches == nil {
			return fail(errors.New("unexpected queue state entry"))
		}
		if info.Size() > s.maxRecord || info.Size() > s.limit-s.used || info.Size() < 44 {
			return fail(ErrStorageCapacity)
		}
		f, err := root.Open(name)
		if err != nil {
			return fail(err)
		}
		data, err := io.ReadAll(io.LimitReader(f, info.Size()+1))
		f.Close()
		if err != nil || int64(len(data)) != info.Size() {
			return fail(ErrStorage)
		}
		record, err := decodeRecord(data)
		if err != nil || record.ID != matches[1] {
			return fail(ErrStorage)
		}
		charge := int64(len(data))
		if record.State == protocol.StateQueued || record.State == protocol.StateLeased {
			if int64(len(data)) > s.maxRecord-int64(MaxResultBytes)-512 || record.Allocation < int64(len(data))+int64(MaxResultBytes)+512 || record.Allocation > s.limit {
				return fail(ErrStorage)
			}
			charge = record.Allocation
		}
		if charge > s.limit-s.used {
			return fail(ErrStorageCapacity)
		}
		s.files[record.ID] = storeFile{int64(len(data)), charge}
		s.used += charge
		records = append(records, record)
	}
	if len(records) > maxJobs {
		return fail(ErrStorageCapacity)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Order < records[j].Order })
	if err = s.syncDirectory(); err != nil {
		return fail(err)
	}
	return s, records, nil
}

func (s *stateStore) save(j *job) error {
	if s == nil {
		return nil
	}
	if s.poisoned {
		return ErrStorage
	}
	if !active(j) {
		j.diskCharge = 0
	}
	data, err := encodeRecord(recordOf(j))
	if err != nil {
		s.poisoned = true
		return ErrStorage
	}
	charge := int64(len(data))
	if active(j) {
		charge = j.diskCharge
		if charge < int64(len(data))+int64(MaxResultBytes)+512 {
			charge = int64(len(data)) + int64(MaxResultBytes) + storeOverhead
		}
		j.diskCharge = charge
		data, err = encodeRecord(recordOf(j))
		if err != nil {
			s.poisoned = true
			return ErrStorage
		}
	}
	if int64(len(data)) > s.maxRecord || active(j) && int64(len(data)) > s.maxRecord-int64(MaxResultBytes)-512 {
		return ErrStorageCapacity
	}
	previous := s.files[j.id]
	if charge > s.limit-(s.used-previous.charge) {
		return ErrStorageCapacity
	}
	temporary := ".job-" + j.id + ".tmp"
	final := "job-" + j.id + ".bin"
	f, err := s.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		n, writeErr := s.write(f, data)
		err = writeErr
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		if err == nil {
			err = s.sync(f)
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = s.rename(temporary, final)
	}
	if err == nil {
		err = s.syncDirectory()
	}
	if err != nil {
		s.poisoned = true
		return ErrStorage
	}
	s.files[j.id] = storeFile{int64(len(data)), charge}
	s.used += charge - previous.charge
	return nil
}

func (s *stateStore) releaseReservation(id string) {
	if s == nil {
		return
	}
	file, ok := s.files[id]
	if ok {
		s.used -= file.charge - file.size
		file.charge = file.size
		s.files[id] = file
	}
}

func (s *stateStore) delete(id string) error {
	if s == nil {
		return nil
	}
	if s.poisoned {
		return ErrStorage
	}
	file, ok := s.files[id]
	if !ok {
		return nil
	}
	if err := s.remove("job-" + id + ".bin"); err != nil {
		s.poisoned = true
		return ErrStorage
	}
	if err := s.syncDirectory(); err != nil {
		s.poisoned = true
		return ErrStorage
	}
	delete(s.files, id)
	s.used -= file.charge
	return nil
}

func (s *stateStore) close() error {
	if s == nil {
		return nil
	}
	var result error
	if s.lock != nil {
		result = errors.Join(result, s.lock.Close())
		s.lock = nil
	}
	if s.root != nil {
		result = errors.Join(result, s.root.Close())
		s.root = nil
	}
	return result
}
