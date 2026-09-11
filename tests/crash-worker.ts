import { Controller } from '../pi-extension/controller.ts';

try {
  const controller = await Controller.fromEnv(process.env.TEST_PI_SESSION!);
  const claim = await controller.execute('issue_claim', {ref:process.env.TEST_ISSUE_UID!});
  process.send?.({ok:true,claim}); // Controller has removed all credentials.
  setInterval(()=>{},1000); // Keep the execution controller alive; simulate abrupt host loss.
} catch {
  process.send?.({ok:false});
  process.exit(1);
}
