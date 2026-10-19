export async function send(url: string, body: string) { return fetch(url, {method: "POST", body}); }
