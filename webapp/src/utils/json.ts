export function parseJSON(json?: string) {
  if (!json) {
    return null;
  }

  try {
    return JSON.parse(json);
  } catch (err) {
    return null;
  }
}
