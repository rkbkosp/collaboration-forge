#!/usr/bin/env node
import { register } from 'tsx/esm/api';
register();
const { main } = await import('../client/main.ts');
process.exitCode = await main(process.argv.slice(2));
