import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export { clearPendingDevice, captureDeviceParams, hasPendingDeviceVisit } from "./pending_device";
export const DevicePage = lazyRoute(() => import("./device_page"));
