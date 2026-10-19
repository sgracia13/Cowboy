import { store } from "../db/store";
export function getSession(id: string) { return store.get(id); }
export function setSession(id: string, v: unknown) { store.set(id, v); }
