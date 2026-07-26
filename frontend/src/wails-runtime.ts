// Wails runtime shim for Vite development
// This file provides the same API as @wails/runtime for development mode
export const Call = {
  ByID: (id, ...args) => Promise.resolve(null),
  ByName: (name, ...args) => Promise.resolve(null),
};

export const Events = {
  On: (event, cb) => () => {},
  Emit: (event, data) => {},
};

export const Create = {
  Array: (fn) => (arr) => (Array.isArray(arr) ? arr.map(fn) : []),
  Object: (fn) => (obj) => fn(obj),
  Any: () => ({}),
  String: () => "",
  Number: () => 0,
  Boolean: () => false,
  Nullable: (fn) => (val) => (val === null ? null : fn(val)),
  Optional: (fn) => (val) => (val === undefined ? undefined : fn(val)),
  Map: (keyType, valueType) => (source) => {
    if (source instanceof Map) return source;
    const result = new Map();
    if (source !== null && typeof source === "object") {
      Object.entries(source).forEach(([k, v]) => result.set(k, v));
    }
    return result;
  },
};

export const CancellablePromise = class extends Promise {
  cancel() {}
};
