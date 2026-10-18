/* Send a test message to a destination and show what happened (the result is in the response, not an HTTP error). */
import { useMutation } from "@tanstack/react-query";
import { Send } from "lucide-react";
import { V02ErrorAlert } from "../../errors";
import { Alert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { Dialog, DialogClose } from "@/components/ui/dialog/dialog";
import { Stack } from "@/components/ui/layout/layout";
import { type Destination, type DestinationScope, testDestination } from "./api";
import { errorClassText } from "./labels";

export function TestDialog({ scope, destination, onClose }: { scope: DestinationScope; destination: Destination; onClose: () => void }) {
  const test = useMutation({ mutationFn: () => testDestination(scope, destination) });
  const result = test.data;
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={`Send a test to ${destination.name}`}
      description="Sends a test message now, through the same address checks and signing as real deliveries. It isn't stored in the delivery log."
      footer={
        <>
          <DialogClose>{result ? "Close" : "Cancel"}</DialogClose>
          <Button loading={test.isPending} onClick={() => test.mutate()}>
            <Send aria-hidden /> {result ? "Send again" : "Send test"}
          </Button>
        </>
      }
    >
      <Stack gap={4}>
        {result && (
          <Alert tone={result.ok ? "success" : "danger"} title={result.ok ? "The destination accepted the test" : "The test failed"}>
            <p>{result.message}</p>
            <p>
              {result.errorClass ? `${errorClassText(result.errorClass)}. ` : ""}
              {result.httpStatus ? `HTTP ${result.httpStatus}. ` : ""}
              {result.durationMs} ms.
            </p>
          </Alert>
        )}
        <V02ErrorAlert error={test.error} title="Couldn't send the test" />
      </Stack>
    </Dialog>
  );
}
