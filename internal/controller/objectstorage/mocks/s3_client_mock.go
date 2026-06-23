package mocks

import (
	"context"
	"reflect"

	"go.uber.org/mock/gomock"
)

type MockS3BucketCreator struct {
	ctrl     *gomock.Controller
	recorder *MockS3BucketCreatorMockRecorder
}

type MockS3BucketCreatorMockRecorder struct {
	mock *MockS3BucketCreator
}

func NewMockS3BucketCreator(ctrl *gomock.Controller) *MockS3BucketCreator {
	mock := &MockS3BucketCreator{ctrl: ctrl}
	mock.recorder = &MockS3BucketCreatorMockRecorder{mock}
	return mock
}

func (m *MockS3BucketCreator) EXPECT() *MockS3BucketCreatorMockRecorder {
	return m.recorder
}

func (m *MockS3BucketCreator) CreateBucket(ctx context.Context, bucket string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CreateBucket", ctx, bucket)
	ret0, _ := ret[0].(error)
	return ret0
}

func (mr *MockS3BucketCreatorMockRecorder) CreateBucket(ctx, bucket any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CreateBucket", reflect.TypeOf((*MockS3BucketCreator)(nil).CreateBucket), ctx, bucket)
}
