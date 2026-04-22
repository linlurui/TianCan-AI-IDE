package providers

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/bits"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

//go:embed ds_pow.wasm
var dsPowWasm []byte

// solvePoW solves a DeepSeek PoW challenge and returns the base64-encoded
// x-ds-pow-response header value.
func solvePoW(challengeData map[string]interface{}, targetPath string) (string, error) {
	algorithm, _ := challengeData["algorithm"].(string)
	challenge, _ := challengeData["challenge"].(string)
	salt, _ := challengeData["salt"].(string)

	difficulty := float64(16)
	if d, ok := challengeData["difficulty"].(float64); ok {
		difficulty = d
	}

	var answer interface{}
	switch algorithm {
	case "DeepSeekHashV1":
		expireAt := float64(0)
		if e, ok := challengeData["expire_at"].(float64); ok {
			expireAt = e
		}
		ans, err := solveDeepSeekHashV1WASM(challenge, salt, expireAt, difficulty)
		if err != nil {
			ans = solveSHA256(challenge, salt, int(difficulty))
		}
		answer = ans
	case "sha256":
		answer = solveSHA256(challenge, salt, int(difficulty))
	default:
		answer = solveSHA256(challenge, salt, int(difficulty))
	}

	// Copy ALL original challenge fields + answer + target_path
	// Matches Python: {**challenge, "answer": answer, "target_path": target_path}
	powData := make(map[string]interface{})
	for k, v := range challengeData {
		powData[k] = v
	}
	powData["answer"] = answer
	powData["target_path"] = targetPath
	jsonBytes, _ := json.Marshal(powData)
	return base64.StdEncoding.EncodeToString(jsonBytes), nil
}

// solveDeepSeekHashV1WASM runs the official DeepSeek WASM binary to solve the PoW.
//
// WASM exports used:
//
//	wasm_solve(retptr, ptrC, lenC, ptrP, lenP, difficulty_f64) → void
//	__wbindgen_add_to_stack_pointer(delta) → i32
//	__wbindgen_export_0(size, align) → i32   (allocator)
//
// Result layout at retptr:
//
//	[retptr+0] i32  status  (1 = success, 0 = failed)
//	[retptr+4] i32  padding
//	[retptr+8] f64  answer  (the nonce)
func solveDeepSeekHashV1WASM(challenge, salt string, expireAt, difficulty float64) (int, error) {
	ctx := context.Background()

	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	mod, err := r.Instantiate(ctx, dsPowWasm)
	if err != nil {
		return 0, fmt.Errorf("wasm instantiate: %w", err)
	}

	mem := mod.Memory()

	addToStack := mod.ExportedFunction("__wbindgen_add_to_stack_pointer")
	alloc := mod.ExportedFunction("__wbindgen_export_0")
	solveFn := mod.ExportedFunction("wasm_solve")

	if addToStack == nil || alloc == nil || solveFn == nil {
		return 0, fmt.Errorf("wasm: missing required exports")
	}

	prefix := fmt.Sprintf("%s_%d_", salt, int64(expireAt))
	cBuf := []byte(challenge)
	pBuf := []byte(prefix)

	// Allocate WASM memory for challenge and prefix strings
	resC, err := alloc.Call(ctx, uint64(len(cBuf)), 1)
	if err != nil || len(resC) == 0 {
		return 0, fmt.Errorf("wasm: alloc challenge: %w", err)
	}
	ptrC := uint32(resC[0])

	resP, err := alloc.Call(ctx, uint64(len(pBuf)), 1)
	if err != nil || len(resP) == 0 {
		return 0, fmt.Errorf("wasm: alloc prefix: %w", err)
	}
	ptrP := uint32(resP[0])

	// Write strings to WASM linear memory
	mem.Write(ptrC, cBuf)
	mem.Write(ptrP, pBuf)

	// Allocate return space on WASM stack (16 bytes)
	resRet, err := addToStack.Call(ctx, api.EncodeI32(-16))
	if err != nil || len(resRet) == 0 {
		return 0, fmt.Errorf("wasm: stack alloc: %w", err)
	}
	retptr := uint32(api.DecodeI32(resRet[0]))

	// Call wasm_solve(retptr, ptrC, lenC, ptrP, lenP, difficulty_f64)
	_, err = solveFn.Call(ctx,
		uint64(retptr),
		uint64(ptrC), uint64(len(cBuf)),
		uint64(ptrP), uint64(len(pBuf)),
		api.EncodeF64(difficulty),
	)
	if err != nil {
		// Restore stack before returning
		addToStack.Call(ctx, api.EncodeI32(16)) //nolint
		return 0, fmt.Errorf("wasm_solve: %w", err)
	}

	// Read result: [retptr+0] i32 status, [retptr+8] f64 answer
	statusBytes, _ := mem.Read(retptr, 4)
	answerBytes, _ := mem.Read(retptr+8, 8)

	// Restore stack
	addToStack.Call(ctx, api.EncodeI32(16)) //nolint

	status := int32(binary.LittleEndian.Uint32(statusBytes))
	answerF64 := math.Float64frombits(binary.LittleEndian.Uint64(answerBytes))

	if status == 0 {
		return 0, fmt.Errorf("DeepSeekHashV1 WASM: solver failed to find solution")
	}
	return int(answerF64), nil
}

// solveSHA256 is the fallback when WASM is unavailable.
// Counts leading zero bits of sha256(salt+challenge+nonce).
func solveSHA256(challenge, salt string, difficulty int) int {
	var targetBits int
	if difficulty > 1000 {
		targetBits = int(math.Floor(math.Log2(float64(difficulty))))
	} else {
		targetBits = difficulty
	}

	saltBytes := []byte(salt)
	challengeBytes := []byte(challenge)
	nonceBuf := make([]byte, 0, 64)

	for nonce := 0; nonce < 1_000_000; nonce++ {
		nonceBuf = nonceBuf[:0]
		nonceBuf = append(nonceBuf, saltBytes...)
		nonceBuf = append(nonceBuf, challengeBytes...)
		nonceBuf = appendInt(nonceBuf, nonce)

		hash := sha256Sum(nonceBuf)

		zeroBits := 0
		for _, b := range hash {
			if b == 0 {
				zeroBits += 8
			} else {
				zeroBits += bits.LeadingZeros8(b)
				break
			}
		}

		if zeroBits >= targetBits {
			return nonce
		}
	}
	return 0
}

// appendInt appends the decimal string representation of n to buf.
func appendInt(buf []byte, n int) []byte {
	if n == 0 {
		return append(buf, '0')
	}
	tmp := make([]byte, 0, 20)
	for n > 0 {
		tmp = append(tmp, byte('0'+n%10))
		n /= 10
	}
	for i, j := 0, len(tmp)-1; i < j; i, j = i+1, j-1 {
		tmp[i], tmp[j] = tmp[j], tmp[i]
	}
	return append(buf, tmp...)
}

func sha256Sum(data []byte) [32]byte {
	return sha256.Sum256(data)
}
