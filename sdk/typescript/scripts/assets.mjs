import { copyFile } from 'node:fs/promises';
await copyFile(new URL('../src/audio-worklet.js', import.meta.url), new URL('../dist/audio-worklet.js', import.meta.url));
