import {readFile} from 'node:fs/promises';
import {YukkuriClient, wavToPCM} from '../../../sdk/typescript/dist/index.js';
const client = new YukkuriClient({baseUrl: process.env.YUKKURI_ENGINE_URL});
const pcm = wavToPCM(await readFile(process.argv[2] ?? 'input.wav'));
console.log((await client.transcribe(pcm)).text);
