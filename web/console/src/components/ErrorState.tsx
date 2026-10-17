import { Button } from "./Button";

export function ErrorState({
  title,
  message,
  onRetry,
}: {
  title: string;
  message: string;
  onRetry?: () => void;
}) {
  return (
    <div className="state-block" role="alert">
      <h2>{title}</h2>
      <p>{message}</p>
      {onRetry ? (
        <Button type="button" onClick={onRetry}>
          Try again
        </Button>
      ) : null}
    </div>
  );
}
