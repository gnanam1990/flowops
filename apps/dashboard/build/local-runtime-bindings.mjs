const LOCAL_RUNTIME_KEYS = [
  "FLOWOPS_CONTROL_API_URL",
  "FLOWOPS_LOCAL_AUTH_ENABLED",
];

export function localRuntimeBindings(environment) {
  return Object.fromEntries(
    LOCAL_RUNTIME_KEYS.flatMap((key) => {
      const value = environment[key]?.trim();
      return value ? [[key, value]] : [];
    }),
  );
}
