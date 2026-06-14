// Hand-written extension to the generated auth/v1 package.
// Added to expose RBAC message types and gRPC stubs for GrantRole, RevokeRole,
// and ListMembers without requiring a full buf generate run.
// When buf generate is next executed this file should be deleted and the
// generated files updated instead.

package authv1

import (
	context "context"

	emptypb "google.golang.org/protobuf/types/known/emptypb"

	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Message types
// ---------------------------------------------------------------------------

// RoleAssignment represents a user's role assignment within a tenant scope.
type RoleAssignment struct {
	// ID is the assignment primary key (UUID string).
	Id string `json:"id,omitempty"`
	// UserId is the user who holds the role.
	UserId string `json:"user_id,omitempty"`
	// Role is the role name: "owner", "admin", "writer", or "reader".
	Role string `json:"role,omitempty"`
	// ScopeType is "org" or "repo".
	ScopeType string `json:"scope_type,omitempty"`
	// ScopeValue is the org name or "org/repo" string.
	ScopeValue string `json:"scope_value,omitempty"`
	// GrantedBy is the actor who created this assignment (UUID string).
	GrantedBy string `json:"granted_by,omitempty"`
}

func (r *RoleAssignment) GetId() string {
	if r != nil {
		return r.Id
	}
	return ""
}

func (r *RoleAssignment) GetUserId() string {
	if r != nil {
		return r.UserId
	}
	return ""
}

func (r *RoleAssignment) GetRole() string {
	if r != nil {
		return r.Role
	}
	return ""
}

func (r *RoleAssignment) GetScopeType() string {
	if r != nil {
		return r.ScopeType
	}
	return ""
}

func (r *RoleAssignment) GetScopeValue() string {
	if r != nil {
		return r.ScopeValue
	}
	return ""
}

func (r *RoleAssignment) GetGrantedBy() string {
	if r != nil {
		return r.GrantedBy
	}
	return ""
}

// GrantRoleRequest assigns a named role to a user within a tenant scope.
type GrantRoleRequest struct {
	TenantId   string `json:"tenant_id,omitempty"`
	UserId     string `json:"user_id,omitempty"`
	Role       string `json:"role,omitempty"`
	ScopeType  string `json:"scope_type,omitempty"`
	ScopeValue string `json:"scope_value,omitempty"`
	GrantedBy  string `json:"granted_by,omitempty"`
}

func (r *GrantRoleRequest) GetTenantId() string {
	if r != nil {
		return r.TenantId
	}
	return ""
}
func (r *GrantRoleRequest) GetUserId() string {
	if r != nil {
		return r.UserId
	}
	return ""
}
func (r *GrantRoleRequest) GetRole() string {
	if r != nil {
		return r.Role
	}
	return ""
}
func (r *GrantRoleRequest) GetScopeType() string {
	if r != nil {
		return r.ScopeType
	}
	return ""
}
func (r *GrantRoleRequest) GetScopeValue() string {
	if r != nil {
		return r.ScopeValue
	}
	return ""
}
func (r *GrantRoleRequest) GetGrantedBy() string {
	if r != nil {
		return r.GrantedBy
	}
	return ""
}

// RevokeRoleRequest removes a specific role assignment by its ID.
type RevokeRoleRequest struct {
	TenantId     string `json:"tenant_id,omitempty"`
	AssignmentId string `json:"assignment_id,omitempty"`
}

func (r *RevokeRoleRequest) GetTenantId() string {
	if r != nil {
		return r.TenantId
	}
	return ""
}
func (r *RevokeRoleRequest) GetAssignmentId() string {
	if r != nil {
		return r.AssignmentId
	}
	return ""
}

// ListMembersRequest queries all role assignments within a tenant scope.
type ListMembersRequest struct {
	TenantId   string `json:"tenant_id,omitempty"`
	ScopeType  string `json:"scope_type,omitempty"`
	ScopeValue string `json:"scope_value,omitempty"`
}

func (r *ListMembersRequest) GetTenantId() string {
	if r != nil {
		return r.TenantId
	}
	return ""
}
func (r *ListMembersRequest) GetScopeType() string {
	if r != nil {
		return r.ScopeType
	}
	return ""
}
func (r *ListMembersRequest) GetScopeValue() string {
	if r != nil {
		return r.ScopeValue
	}
	return ""
}

// ListMembersResponse contains all role assignments for the requested scope.
type ListMembersResponse struct {
	Members []*RoleAssignment `json:"members,omitempty"`
}

func (r *ListMembersResponse) GetMembers() []*RoleAssignment {
	if r != nil {
		return r.Members
	}
	return nil
}

// ---------------------------------------------------------------------------
// gRPC client stubs (extend the existing AuthServiceClient interface via
// a separate extended interface used by management).
// ---------------------------------------------------------------------------

const (
	AuthService_GrantRole_FullMethodName    = "/registry.auth.v1.AuthService/GrantRole"
	AuthService_RevokeRole_FullMethodName   = "/registry.auth.v1.AuthService/RevokeRole"
	AuthService_ListMembers_FullMethodName  = "/registry.auth.v1.AuthService/ListMembers"
)

// authServiceClientExt wraps the base client to add RBAC methods.
// It is returned by NewAuthServiceClientWithRBAC.
type authServiceClientExt struct {
	AuthServiceClient
	cc grpc.ClientConnInterface
}

// NewAuthServiceClientWithRBAC wraps a connection and returns a client that
// implements both the original AuthServiceClient and the RBAC extension methods.
func NewAuthServiceClientWithRBAC(cc grpc.ClientConnInterface) AuthServiceClientWithRBAC {
	return &authServiceClientExt{
		AuthServiceClient: NewAuthServiceClient(cc),
		cc:                cc,
	}
}

// AuthServiceClientWithRBAC extends AuthServiceClient with RBAC management RPCs.
type AuthServiceClientWithRBAC interface {
	AuthServiceClient
	GrantRole(ctx context.Context, in *GrantRoleRequest, opts ...grpc.CallOption) (*emptypb.Empty, error)
	RevokeRole(ctx context.Context, in *RevokeRoleRequest, opts ...grpc.CallOption) (*emptypb.Empty, error)
	ListMembers(ctx context.Context, in *ListMembersRequest, opts ...grpc.CallOption) (*ListMembersResponse, error)
}

func (c *authServiceClientExt) GrantRole(ctx context.Context, in *GrantRoleRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(emptypb.Empty)
	err := c.cc.Invoke(ctx, AuthService_GrantRole_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *authServiceClientExt) RevokeRole(ctx context.Context, in *RevokeRoleRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(emptypb.Empty)
	err := c.cc.Invoke(ctx, AuthService_RevokeRole_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *authServiceClientExt) ListMembers(ctx context.Context, in *ListMembersRequest, opts ...grpc.CallOption) (*ListMembersResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListMembersResponse)
	err := c.cc.Invoke(ctx, AuthService_ListMembers_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// gRPC server stubs — extend UnimplementedAuthServiceServer
// ---------------------------------------------------------------------------

// UnimplementedAuthServiceRBACServer provides default (Unimplemented) responses
// for the RBAC RPCs. Embed alongside UnimplementedAuthServiceServer.
type UnimplementedAuthServiceRBACServer struct{}

func (UnimplementedAuthServiceRBACServer) GrantRole(context.Context, *GrantRoleRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GrantRole not implemented")
}

func (UnimplementedAuthServiceRBACServer) RevokeRole(context.Context, *RevokeRoleRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method RevokeRole not implemented")
}

func (UnimplementedAuthServiceRBACServer) ListMembers(context.Context, *ListMembersRequest) (*ListMembersResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListMembers not implemented")
}

// AuthServiceRBACServer extends AuthServiceServer with the RBAC management RPCs.
type AuthServiceRBACServer interface {
	AuthServiceServer
	GrantRole(context.Context, *GrantRoleRequest) (*emptypb.Empty, error)
	RevokeRole(context.Context, *RevokeRoleRequest) (*emptypb.Empty, error)
	ListMembers(context.Context, *ListMembersRequest) (*ListMembersResponse, error)
}

// RegisterAuthServiceRBACServer registers the combined server on the gRPC registrar.
// It wraps the RBAC server in an adapter that satisfies AuthServiceServer for the
// base ServiceDesc, and registers additional method handlers for the RBAC RPCs.
func RegisterAuthServiceRBACServer(s grpc.ServiceRegistrar, srv AuthServiceRBACServer) {
	s.RegisterService(&authServiceRBAC_ServiceDesc, srv)
}

func _AuthService_GrantRole_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GrantRoleRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AuthServiceRBACServer).GrantRole(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: AuthService_GrantRole_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AuthServiceRBACServer).GrantRole(ctx, req.(*GrantRoleRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _AuthService_RevokeRole_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(RevokeRoleRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AuthServiceRBACServer).RevokeRole(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: AuthService_RevokeRole_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AuthServiceRBACServer).RevokeRole(ctx, req.(*RevokeRoleRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _AuthService_ListMembers_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListMembersRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AuthServiceRBACServer).ListMembers(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: AuthService_ListMembers_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AuthServiceRBACServer).ListMembers(ctx, req.(*ListMembersRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// authServiceRBAC_ServiceDesc is the extended service descriptor that includes
// the original three methods plus the three RBAC methods.
var authServiceRBAC_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "registry.auth.v1.AuthService",
	HandlerType: (*AuthServiceRBACServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "ValidateToken",
			Handler:    _AuthService_ValidateToken_Handler,
		},
		{
			MethodName: "ValidateAPIKey",
			Handler:    _AuthService_ValidateAPIKey_Handler,
		},
		{
			MethodName: "GetUserPermissions",
			Handler:    _AuthService_GetUserPermissions_Handler,
		},
		{
			MethodName: "GrantRole",
			Handler:    _AuthService_GrantRole_Handler,
		},
		{
			MethodName: "RevokeRole",
			Handler:    _AuthService_RevokeRole_Handler,
		},
		{
			MethodName: "ListMembers",
			Handler:    _AuthService_ListMembers_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "auth/v1/auth.proto",
}
