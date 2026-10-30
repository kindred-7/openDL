//go:build gssapi && cgo && (linux || darwin)

package winauth

/*
#cgo linux LDFLAGS: -lgssapi_krb5
#cgo darwin LDFLAGS: -framework GSS
#ifdef __APPLE__
#include <GSS/GSS.h>
#else
#include <gssapi/gssapi.h>
#endif
#include <stdlib.h>
#include <string.h>

static unsigned char opendl_spnego_bytes[] = {0x2b,0x06,0x01,0x05,0x05,0x02};
static unsigned char opendl_krb_bytes[] = {0x2a,0x86,0x48,0x86,0xf7,0x12,0x01,0x02,0x02};
static unsigned char opendl_ntlm_bytes[] = {0x2b,0x06,0x01,0x04,0x01,0x82,0x37,0x02,0x02,0x0a};
static gss_OID_desc opendl_spnego = { sizeof(opendl_spnego_bytes), opendl_spnego_bytes };
static gss_OID_desc opendl_krb = { sizeof(opendl_krb_bytes), opendl_krb_bytes };
static gss_OID_desc opendl_ntlm = { sizeof(opendl_ntlm_bytes), opendl_ntlm_bytes };

typedef struct {
    gss_name_t target;
    gss_cred_id_t cred;
    gss_ctx_id_t context;
    OM_uint32 flags;
    int mechanism;
    int raw_ntlm;
} opendl_gss;

static int opendl_oid_equal(gss_OID a, gss_OID b) {
    return a && b && a->length == b->length && !memcmp(a->elements, b->elements, a->length);
}

static void opendl_close(opendl_gss *s) {
    OM_uint32 minor;
    if (!s) return;
    if (s->context != GSS_C_NO_CONTEXT) gss_delete_sec_context(&minor, &s->context, GSS_C_NO_BUFFER);
    if (s->cred != GSS_C_NO_CREDENTIAL) gss_release_cred(&minor, &s->cred);
    if (s->target != GSS_C_NO_NAME) gss_release_name(&minor, &s->target);
    free(s);
}

static opendl_gss *opendl_new(char *target, size_t size, int only, int raw_ntlm, OM_uint32 *major, OM_uint32 *minor) {
    opendl_gss *s = calloc(1, sizeof(*s));
    if (!s) return NULL;
    s->raw_ntlm = raw_ntlm;
    gss_buffer_desc name = {size, target};
    *major = gss_import_name(minor, &name, GSS_C_NT_HOSTBASED_SERVICE, &s->target);
    if (*major != GSS_S_COMPLETE) { opendl_close(s); return NULL; }
    if (only) {
        gss_OID_set_desc mechs = {1, &opendl_krb};
        gss_OID_set_desc negotiation = {1, &opendl_spnego};
        *major = gss_acquire_cred(minor, GSS_C_NO_NAME, 0, &negotiation, GSS_C_INITIATE, &s->cred, NULL, NULL);
        if (*major != GSS_S_COMPLETE) { opendl_close(s); return NULL; }
        *major = gss_set_neg_mechs(minor, s->cred, &mechs);
        // Only COMPLETE confirms the restricted set was applied. Do not
        // produce a token after an unexpected supplementary status.
        if (*major != GSS_S_COMPLETE) { opendl_close(s); return NULL; }
    }
    return s;
}

static OM_uint32 opendl_step(opendl_gss *s, void *input, size_t input_len, void *binding, size_t binding_len,
                             gss_buffer_t output, OM_uint32 *minor) {
    gss_buffer_desc in = {input_len, input};
    struct gss_channel_bindings_struct cb = {0};
    cb.initiator_addrtype = GSS_C_AF_NULLADDR;
    cb.acceptor_addrtype = GSS_C_AF_NULLADDR;
    cb.application_data.length = binding_len;
    cb.application_data.value = binding;
    gss_OID actual = GSS_C_NO_OID;
    OM_uint32 result = gss_init_sec_context(minor, s->cred, &s->context, s->target,
        s->raw_ntlm ? &opendl_ntlm : &opendl_spnego, s->raw_ntlm ? 0 : GSS_C_MUTUAL_FLAG,
        0, binding_len ? &cb : GSS_C_NO_CHANNEL_BINDINGS,
        input_len ? &in : GSS_C_NO_BUFFER, &actual, output, &s->flags, NULL);
    if (result == GSS_S_COMPLETE) {
        gss_OID context_mech = GSS_C_NO_OID;
        OM_uint32 context_flags = 0;
        int initiated = 0, open = 0;
        OM_uint32 inquire = gss_inquire_context(minor, s->context, NULL, NULL, NULL,
                                                 &context_mech, &context_flags, &initiated, &open);
        if (inquire != GSS_S_COMPLETE) return inquire;
        if (!initiated || !open) return GSS_S_FAILURE;
        int init_mech = opendl_oid_equal(actual, &opendl_krb) ? 1 :
                        opendl_oid_equal(actual, &opendl_ntlm) ? 2 : 0;
        int inquiry_mech = opendl_oid_equal(context_mech, &opendl_krb) ? 1 :
                           opendl_oid_equal(context_mech, &opendl_ntlm) ? 2 : 0;
        if (init_mech && inquiry_mech && init_mech != inquiry_mech) return GSS_S_FAILURE;
        s->mechanism = init_mech ? init_mech : inquiry_mech;
        s->flags = context_flags;
    }
    return result;
}

static int opendl_mutual(opendl_gss *s) { return (s->flags & GSS_C_MUTUAL_FLAG) != 0; }

static OM_uint32 opendl_display_status(OM_uint32 code, int mechanism, OM_uint32 *context, gss_buffer_t out) {
    OM_uint32 minor;
    return gss_display_status(&minor, code, mechanism ? GSS_C_MECH_CODE : GSS_C_GSS_CODE,
                              GSS_C_NO_OID, context, out);
}
*/
import "C"

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unsafe"

	"openDL/i18n"
)

const Available = true

type gssSession struct {
	native       *C.opendl_gss
	bindings     []byte
	kerberosOnly bool
	rawNTLM      bool
	complete     bool
}

// Display a bounded amount of system-provided status text while retaining the
// original codes when the platform cannot describe a mechanism-specific code.
func gssStatusText(code C.OM_uint32, mechanism bool) string {
	if code == 0 {
		return ""
	}
	var context C.OM_uint32
	var parts []string
	which := C.int(0)
	if mechanism {
		which = 1
	}
	for i := 0; i < 4; i++ {
		var buffer C.gss_buffer_desc
		status := C.opendl_display_status(code, which, &context, &buffer)
		if status != C.GSS_S_COMPLETE {
			break
		}
		if buffer.length > 0 && buffer.length <= 1024 && buffer.value != nil {
			value := C.GoStringN((*C.char)(buffer.value), C.int(buffer.length))
			value = strings.TrimSpace(strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return ' '
				}
				return r
			}, value))
			if value != "" {
				parts = append(parts, value)
			}
		}
		var releaseMinor C.OM_uint32
		C.gss_release_buffer(&releaseMinor, &buffer)
		if context == 0 {
			break
		}
	}
	return strings.Join(parts, "; ")
}

func gssFailure(stage string, major, minor C.OM_uint32) error {
	base := fmt.Sprintf(i18n.T("GSSAPI %s失败（0x%08X，0x%08X）"), stage, uint32(major), uint32(minor))
	mainText, detail := gssStatusText(major, false), gssStatusText(minor, true)
	if mainText != "" {
		base += ": " + mainText
	}
	if detail != "" {
		base += "; " + detail
	}
	return fmt.Errorf("%s", base)
}

func New(spn string, kerberosOnly, rawNTLM bool, channelBinding []byte) (Session, error) {
	if rawNTLM && kerberosOnly {
		return nil, i18n.NewError("仅 Kerberos 策略禁止 NTLM 回退")
	}
	service, host, ok := strings.Cut(spn, "/")
	if !ok || service != "HTTP" || host == "" || strings.ContainsAny(host, "/@\x00") {
		return nil, i18n.NewError("服务 SPN 必须采用 HTTP/主机 格式")
	}
	name := []byte(service + "@" + host)
	ptr := C.CBytes(name)
	defer C.free(ptr)
	var major, minor C.OM_uint32
	only := C.int(0)
	if kerberosOnly {
		only = 1
	}
	raw := C.int(0)
	if rawNTLM {
		raw = 1
	}
	native := C.opendl_new((*C.char)(ptr), C.size_t(len(name)), only, raw, &major, &minor)
	if native == nil {
		return nil, gssFailure(i18n.T("初始化"), major, minor)
	}
	return &gssSession{native: native, bindings: append([]byte(nil), channelBinding...), kerberosOnly: kerberosOnly, rawNTLM: rawNTLM}, nil
}

func (s *gssSession) Next(input []byte) ([]byte, bool, error) {
	if s.native == nil || (s.complete && len(input) == 0) || len(input) > 1<<20 {
		return nil, false, i18n.NewError("GSSAPI 认证上下文状态无效")
	}
	if s.rawNTLM && len(input) > 0 && !bytes.HasPrefix(input, []byte("NTLMSSP\x00")) {
		return nil, false, i18n.NewError("GSSAPI NTLM 令牌格式不受支持")
	}
	var in, binding unsafe.Pointer
	if len(input) > 0 {
		in = C.CBytes(input)
		defer C.free(in)
	}
	if len(s.bindings) > 0 {
		binding = C.CBytes(s.bindings)
		defer C.free(binding)
	}
	var output C.gss_buffer_desc
	var minor C.OM_uint32
	major := C.opendl_step(s.native, in, C.size_t(len(input)), binding, C.size_t(len(s.bindings)), &output, &minor)
	defer C.gss_release_buffer(&minor, &output)
	if major != C.GSS_S_COMPLETE && major != C.GSS_S_CONTINUE_NEEDED {
		return nil, false, gssFailure(i18n.T("认证"), major, minor)
	}
	if output.length > 1<<20 {
		return nil, false, i18n.NewError("GSSAPI 认证令牌超过大小限制")
	}
	var token []byte
	if output.length > 0 {
		token = C.GoBytes(output.value, C.int(output.length))
	}
	if s.rawNTLM && len(token) > 0 && !bytes.HasPrefix(token, []byte("NTLMSSP\x00")) {
		return nil, false, i18n.NewError("GSSAPI NTLM 令牌格式不受支持")
	}
	s.complete = major == C.GSS_S_COMPLETE
	return token, s.complete, nil
}

func (s *gssSession) Package() (string, error) {
	if s.native == nil || !s.complete {
		return "", i18n.NewError("GSSAPI 认证尚未完成")
	}
	switch s.native.mechanism {
	case 1:
		if C.opendl_mutual(s.native) == 0 {
			return "", i18n.NewError("Kerberos 未完成服务端身份验证")
		}
		return "Kerberos", nil
	case 2:
		if !s.kerberosOnly {
			return "NTLM", nil
		}
	}
	return "", i18n.NewError("GSSAPI 协商选择了不支持的认证协议")
}

func (s *gssSession) Close() {
	if s.native != nil {
		C.opendl_close(s.native)
		s.native = nil
	}
	clear(s.bindings)
}
