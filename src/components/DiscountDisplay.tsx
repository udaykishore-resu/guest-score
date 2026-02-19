import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

interface DiscountDisplayProps {
  score: number;
}

export const DiscountDisplay = ({ score }: DiscountDisplayProps) => {
  const calculateDiscount = (score: number): number => {
    if (score >= 90) return 20; // 20% discount for excellent scores
    if (score >= 70) return 15; // 15% discount for good scores
    if (score >= 50) return 10; // 10% discount for fair scores
    return 0; // No discount for poor scores
  };

  const discount = calculateDiscount(score);

  return (
    <Card>
      <CardHeader>
        <CardTitle>Discount Eligibility</CardTitle>
      </CardHeader>
      <CardContent className="space-y-2">
        <div className="flex items-center justify-between">
          <span className="text-lg font-medium text-muted-foreground">
            Available Discount
          </span>
          <span className="text-3xl font-bold text-primary">
            {discount}%
          </span>
        </div>
        <p className="text-sm text-muted-foreground">
          {discount > 0 
            ? `You are eligible for a ${discount}% discount on your next stay!`
            : "Improve your guest score to unlock discounts!"}
        </p>
      </CardContent>
    </Card>
  );
};