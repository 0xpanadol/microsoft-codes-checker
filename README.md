# Microsoft Store Code Checker

A Go program to check Microsoft Store codes and save the results in organized text files. (Modify the time.Sleep to higher number when you don't have enough WLIDs so you don't get timeout early)

## Features

- Organized output in separate text files (good.txt, bad.txt, error.txt)
- Beautiful console output with colored summary
- Thread-safe file operations
- Random WLID selection for authentication to dodge the timeout

## Requirements

- Go 1.21 or later
- `input/codes.txt` file containing codes to check (one per line)
- `input/wlids.txt` file containing WLIDs for authentication (one per line)

## Installation

1. Clone the repository
2. Run `go mod tidy` to download dependencies
3. Create `input/codes.txt` with your codes (one per line)
4. Create `input/wlids.txt` with your WLIDs (one per line)

## Usage

1. Make sure you have the required files:

   - `input/codes.txt`: One code per line
   - `input/wlids.txt`: One WLID per line

2. Run the program:

   ```bash
   go run main.go
   ```

3. Results will be saved in the `output` folder:
   - `output/good.txt`: List of valid codes
   - `output/expired.txt`: List of expired codes
   - `output/redeemed.txt`: List of redeemed codes
   - `output/bad.txt`: List of invalid codes
   - `output/error.txt`: List of codes that failed to check
   - `output/retry.txt`: List of codes that you need to retry with (WLID got timeout cause of too many requests)

## Output

The program provides:

- Real-time progress in a table format
- Color-coded summary of results
- Codes saved in text files, one per line
- Error handling and logging

## Example

Input files:

`input/codes.txt`:

```
KRGQP-HDM6W-J6HW3-V44PH-XXXXX
XXXXX-XXXXX-XXXXX-XXXXX-XXXXX
```

`input/wlids.txt`:

```
WLID1.0=t=...
WLID1.0=t=...
```

Output files:

`output/good.txt`:

```
KRGQP-HDM6W-J6HW3-V44PH-XXXXX
```

`output/bad.txt`:

```
XXXXX-XXXXX-XXXXX-XXXXX-XXXXX
```

Console output:

```
Code                            Status    Details
KRGQP-HDM6W-J6HW3-V44PH-XXXXX  good      {"tokenType":"Others","value":null,...
XXXXX-XXXXX-XXXXX-XXXXX-XXXXX   bad      {"events":{"cart":[{"data":{"reason":"RedeemTokenExpired"...

Summary:
Good codes: 1
Bad codes: 1
Errors: 0
```
