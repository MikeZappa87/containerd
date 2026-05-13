/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package networking

// DeleteNetdevRequest is the request message for the DeleteNetdev RPC.
//
// NOTE: This type is defined manually (not via protoc) to avoid
// regenerating the entire protobuf descriptor. It is wire-compatible
// with the proto definition added to networking.proto.
type DeleteNetdevRequest struct {
	// Sandbox identifier.
	SandboxId string `protobuf:"bytes,1,opt,name=sandbox_id,json=sandboxId,proto3" json:"sandbox_id,omitempty"`
	// Name of the device to delete.
	Name string `protobuf:"bytes,2,opt,name=name,proto3" json:"name,omitempty"`
	// When true, delete from the host namespace instead of the pod netns.
	HostNetwork bool `protobuf:"varint,3,opt,name=host_network,json=hostNetwork,proto3" json:"host_network,omitempty"`
}

func (x *DeleteNetdevRequest) Reset()         { *x = DeleteNetdevRequest{} }
func (x *DeleteNetdevRequest) String() string { return x.SandboxId + "/" + x.Name }
func (x *DeleteNetdevRequest) ProtoMessage()  {}

func (x *DeleteNetdevRequest) GetSandboxId() string {
	if x != nil {
		return x.SandboxId
	}
	return ""
}

func (x *DeleteNetdevRequest) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *DeleteNetdevRequest) GetHostNetwork() bool {
	if x != nil {
		return x.HostNetwork
	}
	return false
}

// DeleteNetdevResponse is the response message for the DeleteNetdev RPC.
type DeleteNetdevResponse struct{}

func (x *DeleteNetdevResponse) Reset()         { *x = DeleteNetdevResponse{} }
func (x *DeleteNetdevResponse) String() string { return "DeleteNetdevResponse" }
func (x *DeleteNetdevResponse) ProtoMessage()  {}
